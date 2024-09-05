const axios = require('axios');
const fs = require('fs');
const https = require('https');
const path = require('path');
const cheerio = require('cheerio');

const httpClient = axios.create({
  httpsAgent: new https.Agent({
    rejectUnauthorized: false,
  }),
});

const baseURL = 'https://putusan3.mahkamahagung.go.id';
const totalPage = 1;

const folderPath = path.join(__dirname, 'yurisprudensi');
if (!fs.existsSync(folderPath)) {
  fs.mkdirSync(folderPath);
}

async function downloadPDF(url, filename) {
  try {
    const response = await httpClient({
      url,
      method: 'GET',
      responseType: 'stream',
    });

    const filePath = path.join(folderPath, filename);

    const writer = fs.createWriteStream(filePath);
    response.data.pipe(writer);

    writer.on('finish', () => {
      console.log('Downloaded PDF:', filename);
    });

    writer.on('error', (err) => {
      console.error('Error writing file:', err);
    });
  } catch (err) {
    console.error('Error downloading PDF:', err);
  }
}

async function main() {
  for (let page = 1; page <= totalPage; page++) {
    const url = `${baseURL}/yurisprudensi/index/page/${page}.html`;
    console.log('URL :', url);

    try {
      const listResp = await httpClient.get(url);
      const listHtmlContent = listResp.data;

      // Load html content
      const $listHtmlContent = cheerio.load(listHtmlContent);

      const detailUrlRegex =
        /^https:\/\/putusan3\.mahkamahagung\.go\.id\/yurisprudensi\/detail\/.*\.html$/;

      const detailUrls = [];
      // Get all detail urls on this page
      $listHtmlContent('a').each((_, element) => {
        const href = $listHtmlContent(element).attr('href');
        const hasClass = $listHtmlContent(element).attr('class');
        if (href && !hasClass && detailUrlRegex.test(href)) {
          detailUrls.push(href);
        }
      });

      // Loops every detail page
      for (let i = 0; i < detailUrls.length; i++) {
        const detailResp = await httpClient.get(detailUrls[i]);
        const detailHtmlContent = detailResp.data;

        const $detailHtmlContent = cheerio.load(detailHtmlContent);

        const targetDiv = $detailHtmlContent('div.card.bg-success.mb-3').filter(
          (_, el) => {
            return $detailHtmlContent(el).text().includes('Sumber Putusan');
          }
        );

        const url = targetDiv.find('a').attr('href');

        if (url) {
          const detailPutusan = await httpClient.get(url);
          const detailPutusanHtmlContent = detailPutusan.data;

          const $detailPutusanHtmlContent = cheerio.load(
            detailPutusanHtmlContent
          );

          const pdfUrlRegex =
            /^https:\/\/putusan3\.mahkamahagung\.go\.id\/direktori\/download_file\/[^\/]+\/pdf\/[^\/]+$/;

          // Get all pdf urls on this page
          $detailPutusanHtmlContent('a').each((_, element) => {
            const href = $detailPutusanHtmlContent(element).attr('href');
            const name = $detailPutusanHtmlContent(element).text();
            if (href && pdfUrlRegex.test(href)) {
              downloadPDF(href, name.replaceAll('/', '_'));
            }
          });
        } else {
          console.log('No pdf link found');
        }
      }
    } catch (err) {
      console.error('Error:', err);
    }
  }
}

main();