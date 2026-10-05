// Scroll reveals, the claim box, the mobile menu and, when a page was not prerendered (local
// preview), the shared header and footer from chrome.js. Every page loads chrome.js first.
const { LINKS, navHtml, footerHtml } = window.PoweurChrome;

const root = document.documentElement.dataset.root || "";
const here = location.pathname.replace(/index\.html$/, "");

const nav = document.getElementById("nav");
if (nav) {
  // Deploys prerender the header into the HTML; a local preview builds it here.
  if (!nav.querySelector(".wrap")) {
    nav.className = "nav";
    nav.innerHTML = navHtml(root, here);
  }
  const toggle = nav.querySelector(".nav-toggle");
  toggle.addEventListener("click", () => {
    const open = nav.classList.toggle("open");
    toggle.setAttribute("aria-expanded", String(open));
  });
  nav.querySelectorAll("ul a").forEach((a) => a.addEventListener("click", () => nav.classList.remove("open")));
}

const footer = document.getElementById("footer");
if (footer && !footer.querySelector(".wrap")) footer.innerHTML = footerHtml(root, new Date().getFullYear());

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
