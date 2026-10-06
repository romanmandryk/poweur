// The site's shared header and footer markup. Loaded before site.js on every page, and by
// scripts/prerender-chrome.mjs at deploy time, which writes the same markup into each page so the
// first paint already has its navigation (without that, the header is empty until JavaScript runs).
(function (g) {
  const LINKS = {
    docs: "/docs", // the Docusaurus build, served under the website (apps/docs, baseUrl /docs/)
    github: "https://github.com/romanmandryk/poweur",
    app: "https://poweur.net/app/",
  };

  function navLink(root, here, href, label, extra = "") {
    const url = /^(https?:|#|\/)/.test(href) ? href : root + href;
    const current = !href.startsWith("http") && here.endsWith(href.replace(/index\.html$/, "")) && href !== "index.html" && !href.startsWith("/") ? ' aria-current="page"' : "";
    return `<li><a href="${url}"${current} ${extra}>${label}</a></li>`;
  }

  /** The inside of <header id="nav">. `here` is the page's path, e.g. "/faq/". */
  function navHtml(root, here) {
    return `<div class="wrap">
      <a class="logo" href="${root}index.html" aria-label="Poweur home"><img src="${root}assets/brand/poweur-horizontal-no-id-on-dark.svg" alt="Poweur" width="81" height="22"></a>
      <ul>
        ${navLink(root, here, "index.html#primitives", "Product")}
        ${navLink(root, here, "index.html#use-cases", "Use cases")}
        ${navLink(root, here, "architecture.html", "Architecture")}
        ${navLink(root, here, "blog/", "Blog")}
        ${navLink(root, here, "faq/", "FAQ")}
        ${navLink(root, here, LINKS.docs, "Docs", 'class="ext" target="_blank" rel="noopener"')}
        ${navLink(root, here, LINKS.github, "GitHub", 'class="ext" target="_blank" rel="noopener"')}
      </ul>
      <span class="spacer"></span>
      <a class="btn btn-sm btn-ghost nav-cta" href="${LINKS.app}">Open app</a>
      <a class="btn btn-sm btn-primary nav-cta" href="${root}index.html#claim">Claim your ID</a>
      <button class="nav-toggle" aria-label="Menu" aria-expanded="false">☰</button>
    </div>`;
  }

  /** The inside of <footer id="footer">. */
  function footerHtml(root, year) {
    return `<div class="wrap">
      <div class="foot">
        <div class="brand">
          <img src="${root}assets/brand/poweur-horizontal-no-id-on-dark.svg" alt="Poweur" style="height:24px;width:auto">
          <p>One open ID for identity, messages and data. Open source, self-hostable, yours.</p>
        </div>
        <div><h3>Product</h3><ul>
          <li><a href="${root}index.html#primitives">ID, messaging &amp; files</a></li>
          <li><a href="${root}index.html#use-cases">Use cases</a></li>
          <li><a href="${root}index.html#apps">Poweured apps</a></li>
          <li><a href="${root}index.html#hosting">Hosting options</a></li>
          <li><a href="${root}index.html#try">Try it live</a></li>
          <li><a href="${LINKS.app}">Web app</a></li>
        </ul></div>
        <div><h3>Developers</h3><ul>
          <li><a href="${LINKS.docs}">Documentation</a></li>
          <li><a href="${root}architecture.html">Architecture</a></li>
          <li><a href="${LINKS.docs}/clients/js-sdk">TypeScript SDK</a></li>
          <li><a href="${LINKS.docs}/clients/cli-reference">CLI</a></li>
          <li><a href="${LINKS.docs}/auth/add-sign-in">Sign in with Poweur</a></li>
        </ul></div>
        <div><h3>Project</h3><ul>
          <li><a href="${LINKS.github}">GitHub</a></li>
          <li><a href="${LINKS.github}/tree/master/epics">Roadmap</a></li>
          <li><a href="${LINKS.github}/blob/master/CONTRIBUTING.md">Contributing</a></li>
          <li><a href="${root}blog/">Blog</a></li>
          <li><a href="${root}faq/">FAQ</a></li>
          <li><a href="${root}threat-model/">Threat model</a></li>
          <li><a href="${root}press/">Press kit</a></li>
          <li><a href="${root}legal/#security">Security</a></li>
        </ul></div>
        <div><h3>Community</h3><ul>
          <li><a href="${LINKS.github}/discussions">Discussions</a></li>
        </ul></div>
      </div>
      <div class="foot-bottom">
        <span>© ${year} Poweur contributors · Open source · <a href="${root}legal/privacy/">Privacy</a> · <a href="${root}legal/terms/">Terms</a> · <a href="${root}legal/">Legal &amp; contact</a></span>
        <span>Poweur proves control of a name and its keys, not a legal identity.</span>
      </div>
    </div>`;
  }

  g.PoweurChrome = { LINKS, navHtml, footerHtml };
})(typeof globalThis !== "undefined" ? globalThis : window);
