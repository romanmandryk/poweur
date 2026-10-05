// Shared nav + footer, scroll reveals. No build step: every page includes this file.
const LINKS = {
  docs: "/docs", // the Docusaurus build, served under the website (apps/docs, baseUrl /docs/)
  github: "https://github.com/romanmandryk/poweur",
  app: "https://poweur.net/app/",
};

const root = document.documentElement.dataset.root || "";
const here = location.pathname.replace(/index\.html$/, "");

function navLink(href, label, extra = "") {
  const url = /^(https?:|#|\/)/.test(href) ? href : root + href;
  const current = !href.startsWith("http") && here.endsWith(href.replace(/index\.html$/, "")) && href !== "index.html" && !href.startsWith("/") ? ' aria-current="page"' : "";
  return `<li><a href="${url}"${current} ${extra}>${label}</a></li>`;
}

const nav = document.getElementById("nav");
if (nav) {
  nav.className = "nav";
  nav.innerHTML = `
    <div class="wrap">
      <a class="logo" href="${root}index.html" aria-label="Poweur home"><img src="${root}assets/brand/poweur-horizontal-no-id-on-dark.svg" alt="Poweur"></a>
      <ul>
        ${navLink("index.html#primitives", "Product")}
        ${navLink("index.html#use-cases", "Use cases")}
        ${navLink("architecture.html", "Architecture")}
        ${navLink("faq/", "FAQ")}
        ${navLink(LINKS.docs, "Docs", 'class="ext" target="_blank" rel="noopener"')}
        ${navLink(LINKS.github, "GitHub", 'class="ext" target="_blank" rel="noopener"')}
      </ul>
      <span class="spacer"></span>
      <a class="btn btn-sm btn-ghost nav-cta" href="${LINKS.app}">Open app</a>
      <a class="btn btn-sm btn-primary nav-cta" href="${root}index.html#claim">Claim your ID</a>
      <button class="nav-toggle" aria-label="Menu" aria-expanded="false">☰</button>
    </div>`;
  const toggle = nav.querySelector(".nav-toggle");
  toggle.addEventListener("click", () => {
    const open = nav.classList.toggle("open");
    toggle.setAttribute("aria-expanded", String(open));
  });
  nav.querySelectorAll("ul a").forEach((a) => a.addEventListener("click", () => nav.classList.remove("open")));
}

// The Blog link appears only when the site was built with a published post (blog/posts.json).
fetch(root + "blog/posts.json")
  .then((r) => (r.ok ? r.json() : []))
  .then((posts) => {
    if (!posts.length) return;
    const list = document.querySelector("#nav ul");
    // Blog sits right after Architecture, before FAQ.
    const architecture = [...document.querySelectorAll("#nav ul a")].find((a) => /architecture\.html$/.test(a.getAttribute("href") || ""));
    if (architecture) architecture.parentElement.insertAdjacentHTML("afterend", navLink("blog/", "Blog"));
    else if (list) list.insertAdjacentHTML("beforeend", navLink("blog/", "Blog"));
    const project = [...document.querySelectorAll("#footer h3")].find((h) => h.textContent === "Project");
    if (project) project.nextElementSibling.insertAdjacentHTML("afterbegin", `<li><a href="${root}blog/">Blog</a></li>`);
  })
  .catch(() => {});

const footer = document.getElementById("footer");
if (footer) {
  footer.innerHTML = `
    <div class="wrap">
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
        <span>© ${new Date().getFullYear()} Poweur contributors · Open source · <a href="${root}legal/privacy/">Privacy</a> · <a href="${root}legal/terms/">Terms</a> · <a href="${root}legal/">Legal &amp; contact</a></span>
        <span>Poweur proves control of a name and its keys, not a legal identity.</span>
      </div>
    </div>`;
}

// The claim box hands the typed name to the app, which pre-fills it (?handle=).
document.querySelectorAll("form.claim").forEach((form) => {
  form.addEventListener("submit", (event) => {
    event.preventDefault();
    const handle = (new FormData(form).get("handle") || "").toString().trim().toLowerCase();
    const url = new URL(LINKS.app);
    if (/^[a-z0-9][a-z0-9-]{0,62}$/.test(handle)) url.searchParams.set("handle", handle);
    location.href = url.toString();
  });
});

// Duplicate marquee content so the loop is seamless.
document.querySelectorAll(".marquee-track").forEach((t) => (t.innerHTML += t.innerHTML));

// Reveal on scroll.
const io = new IntersectionObserver(
  (entries) => entries.forEach((e) => e.isIntersecting && (e.target.classList.add("in"), io.unobserve(e.target))),
  { rootMargin: "0px 0px -8% 0px" }
);
document.querySelectorAll(".reveal").forEach((el) => io.observe(el));
