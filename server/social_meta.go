package server

// ogImageFile is the 1200x630 social preview card shipped in assets/.
const ogImageFile = "og-image.png"

// socialMetaHTML renders the Open Graph / Twitter card tags for the landing
// page. Crawlers read og:image as an absolute URL, so the image lives under the
// brand asset route (served from ./assets or /opt/etunnel/assets, #38) and is
// addressed through the configured base domain - never r.Host, which a visitor
// controls on tenant hostnames.
func socialMetaHTML(cfg Config, lang, at, title, desc string) string {
	base := "https://" + cfg.Domain
	img := base + "/assets/" + ogImageFile
	url := base + at
	q := func(s string) string { return htmlEscapeText(s) }
	return `<meta property="og:type" content="website">` +
		`<meta property="og:site_name" content="etunnel">` +
		`<meta property="og:title" content="` + q(title) + `">` +
		`<meta property="og:description" content="` + q(desc) + `">` +
		`<meta property="og:url" content="` + url + `">` +
		`<meta property="og:image" content="` + img + `">` +
		`<meta property="og:image:width" content="1200">` +
		`<meta property="og:image:height" content="630">` +
		`<meta property="og:image:alt" content="etunnel - your localhost, on the internet">` +
		`<meta name="twitter:card" content="summary_large_image">` +
		`<meta name="twitter:title" content="` + q(title) + `">` +
		`<meta name="twitter:description" content="` + q(desc) + `">` +
		`<meta name="twitter:image" content="` + img + `">`
}
