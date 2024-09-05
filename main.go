package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	cloud_storage "cloud.google.com/go/storage"
	"github.com/gabriel-vasile/mimetype"
	"github.com/pkg/errors"
	"google.golang.org/api/option"
)

type PdfError struct {
	Page  int
	Error string
}

var (
	pageData   = map[int]int{}
	errorPdf   = map[string]PdfError{}
	baseURL    = "https://jdih.mahkamahagung.go.id/dokumen-hukum/putusan?page="
	totalPage  = 139
	folderName = "yurisprudensi"
)

func main() {
	NewStorage()

	// Create a custom HTTP client that skips SSL verification
	httpClient := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}

	for page := 88; page <= totalPage; page++ {
		fmt.Printf("=+=+=+=+=page %d=+=+=+=+=\n", page)

		url := baseURL + strconv.Itoa(page)
		resp, err := httpClient.Get(url)
		if err != nil {
			log.Fatal(err)
		}
		defer resp.Body.Close()

		htmlContent, err := io.ReadAll(resp.Body)
		if err != nil {
			log.Fatal(err)
		}

		r := regexp.MustCompile(`<a class="d-inline-block" href="(https://jdih\.mahkamahagung\.go\.id/legal-product/[^"]+/detail)">\s*<h2>(.*?)<\/h2>`)
		matches := r.FindAllStringSubmatch(string(htmlContent), -1)

		pageData[page] = len(matches)

		if len(matches) == 0 {
			break
		}

		for _, match := range matches {
			url := match[1] // Extracted URL

			detailResp, err := httpClient.Get(url)
			if err != nil {
				log.Fatal(err)
			}
			defer detailResp.Body.Close()

			detailContent, err := io.ReadAll(detailResp.Body)
			if err != nil {
				log.Fatal(err)
			}

			pdfR := regexp.MustCompile(`https:\/\/.*?\.pdf`)
			pdfURL := pdfR.FindString(string(detailContent))
			fmt.Println("Found PDF URL:", pdfURL)

			parts := strings.Split(pdfURL, "/")
			title := parts[len(parts)-1] // Extracted title
			// title = strings.ReplaceAll(title, " ", "-")
			// title = strings.ToLower(title)

			fileReader, err := downloadPDF(pdfURL, title)
			if err != nil {
				log.Fatal(err)
			}

			err = uploadToCloudStorage(fileReader, title)
			if err != nil {
				errorPdf[title] = PdfError{
					Page:  page,
					Error: err.Error(),
				}
				if err.Error() != "type not allowed" {
					fmt.Println(errorPdf)
					fmt.Println(pageData)
					log.Fatal(err)
				}
				continue
			}
		}
	}
	fmt.Println(errorPdf)
}

func downloadPDF(url string, filename string) (multipart.File, error) {
	dirPath := "./" + folderName

	err := os.MkdirAll(dirPath, os.ModePerm)
	if err != nil {
		log.Fatal(err)
		return nil, err
	}

	// Create a custom HTTP client that skips SSL verification
	httpClient := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}

	resp, err := httpClient.Get(url)
	if err != nil {
		log.Fatal(err)
		return nil, err
	}
	defer resp.Body.Close()

	// Define the file path
	filepath := dirPath + "/" + filename

	file, err := os.Create(filepath)
	if err != nil {
		log.Fatal(err)
		return nil, err
	}
	defer file.Close()

	_, err = io.Copy(file, resp.Body)
	if err != nil {
		log.Fatal(err)
		return nil, err
	}

	fmt.Println("Downloaded:", filepath)

	// Reopen the file to return it as a multipart.File
	fileToUpload, err := os.Open(filepath)
	if err != nil {
		return nil, err
	}

	return fileToUpload, nil
}

func uploadToCloudStorage(file multipart.File, fileName string) error {
	ctx := context.Background()

	// Assuming you have initialized the cloud storage client in NewStorage()
	_, err := Storage.StoreObject(ctx, file, fileName)
	return err
}

type (
	GCSConfiguration struct {
		Bucket              string
		Credential          string
		UrlExpiringDuration time.Duration
	}

	storage struct {
		config   GCSConfiguration
		bucket   *cloud_storage.BucketHandle
		uploader uploader
	}
	defaultUploader struct {
		bucket *cloud_storage.BucketHandle
	}
	storageOpt struct {
		uploader uploader
	}
	storageOptFn func(*storageOpt)
	uploader     interface {
		upload(context.Context, string, io.Reader, string) error
	}
	// sizer interface {
	// 	Size() int64
	// }
)

var (
	conf        GCSConfiguration
	Storage     *storage
	storageOnce sync.Once
	allowType   = map[string]bool{
		"application/pdf": true,
		"image/jpg":       true,
		"image/jpeg":      true,
		"image/png":       true,
		"image/gif":       true,
		"image/svg+xml":   true,
		"application/vnd.openxmlformats-officedocument.wordprocessingml.document":   true,
		"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":         true,
		"application/vnd.openxmlformats-officedocument.presentationml.presentation": true,
	}
	// maxSizeInMB int64 = 5 * 1024 * 1024
)

func NewStorage(opts ...storageOptFn) {
	conf.Bucket = "hukumku-prod-bucket"
	conf.Credential = "../config/google-cloud-storage-creds-prod.json"
	conf.UrlExpiringDuration = time.Duration(900000000000)

	storageOnce.Do(func() {
		client, err := cloud_storage.NewClient(context.Background(), option.WithCredentialsFile(conf.Credential))
		if err != nil {
			panic(errors.Wrap(err, "fail to init cloud storage"))
		}

		bucket := client.Bucket(conf.Bucket)
		opt := storageOpt{
			uploader: &defaultUploader{
				bucket: bucket,
			},
		}
		for _, o := range opts {
			o(&opt)
		}

		Storage = &storage{conf, bucket, opt.uploader}

		log.Println("Cloud Storage service ready")
	})
}

func (s *storage) StoreObject(ctx context.Context, file multipart.File, fileName string) (mime string, err error) {
	mimeType, err := s.ValidateFileTypeAndSize(ctx, file)
	if err != nil {
		return "", err
	}

	err = s.uploader.upload(ctx, mimeType, file, fileName)
	if err != nil {
		return "", err
	}

	fmt.Printf("File uploaded to '%s' in bucket '%s'\n", fileName, s.config.Bucket)

	return mimeType, nil
}

func (s *storage) ValidateFileTypeAndSize(ctx context.Context, file multipart.File) (string, error) {
	// Obtain the file size using FileInfo
	_, err := file.(*os.File).Stat()
	if err != nil {
		return "", err
	}

	// Check file size
	// if fileInfo.Size() > maxSizeInMB {
	// 	return "", errors.New("size exceeded")
	// }

	// Read the file header to detect MIME type
	fileHeader := make([]byte, 512)
	if _, err := file.Read(fileHeader); err != nil {
		return "", err
	}

	// Reset the file pointer to the beginning
	if _, err := file.Seek(0, 0); err != nil {
		return "", err
	}

	// Detect MIME type
	mimeType := mimetype.Detect(fileHeader)
	if !allowType[mimeType.String()] {
		return "", errors.New("type not allowed")
	}

	return mimeType.String(), nil
	// fileHeader := make([]byte, file.(sizer).Size())
	// if _, err := file.Read(fileHeader); err != nil {
	// 	return "", err
	// }

	// if _, err := file.Seek(0, 0); err != nil {
	// 	return "", err
	// }

	// if file.(sizer).Size() > maxSizeInMB {
	// 	return "", errors.New("size exceeded")
	// }

	// mimeType := mimetype.Detect(fileHeader)

	// if !allowType[mimeType.String()] {
	// 	return "", errors.New("type not allowed")
	// }

	// return mimeType.String(), nil
}

func (u *defaultUploader) upload(ctx context.Context, mimeType string, file io.Reader, fileName string) error {
	// fileName := uuid.NewString()
	fullPath := fmt.Sprintf("%s/%s", folderName, fileName) // NEW: Include folder name in the path

	obj := u.bucket.Object(fullPath)
	writeObject := obj.NewWriter(ctx)
	writeObject.ContentType = mimeType

	defer writeObject.Close()

	_, err := io.Copy(writeObject, file)
	if err != nil {
		return err
	}

	return nil
}
