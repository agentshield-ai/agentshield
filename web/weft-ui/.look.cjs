// temp helper (removed at the end): node .look.cjs <outdir> <width> <scheme> path...
const { chromium } = require('@playwright/test')
;(async () => {
  const [outdir, width, scheme, ...paths] = process.argv.slice(2)
  const browser = await chromium.launch()
  const ctx = await browser.newContext({ viewport: { width: Number(width), height: 900 }, colorScheme: scheme })
  const page = await ctx.newPage()
  const errors = []
  page.on('pageerror', (e) => errors.push(`${page.url()} :: ${e.message}`))
  page.on('console', (m) => m.type() === 'error' && errors.push(`${page.url()} :: console: ${m.text().slice(0, 300)}`))
  for (const p of paths) {
    await page.goto('http://127.0.0.1:5210' + p, { waitUntil: 'networkidle' }).catch((e) => errors.push(`${p} goto ${e.message}`))
    await page.waitForTimeout(500)
    const name = p.replace(/[^a-z0-9]+/gi, '_').slice(0, 80) + `_${width}_${scheme}.png`
    const ov = await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth + 1 || [...document.querySelectorAll('.main')].some((m) => m.scrollWidth > m.clientWidth + 1))
    if (ov) errors.push(`${p} HORIZONTAL OVERFLOW`)
    await page.addStyleTag({ content: 'html,body,#root,.app{height:auto!important} .main{overflow:visible!important}' })
    await page.waitForTimeout(150)
    await page.screenshot({ path: `${outdir}/${name}`, fullPage: true })
    console.log('shot', name)
  }
  console.log(errors.length ? errors.join('\n') : 'no errors')
  await browser.close()
})()
