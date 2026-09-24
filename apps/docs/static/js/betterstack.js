// Better Stack web analytics (traffic for poweur.org and its docs). The same file lives in
// apps/site/assets/ (website) and apps/docs/static/js/ (docs). Skipped on local previews.
if (!/^(localhost|127\.0\.0\.1|\[::1\])$/.test(location.hostname)) {
  !function(b,e,t,r){
    b[t]=b[t]||function(...args){(b[t].q=b[t].q||[]).push(args)};
    b[t].l=+new Date;
    var s=e.createElement('script'); s.async=1; s.crossOrigin='anonymous';
    s.src='https://betterstack.net/b.js?t='+r;
    (e.head||e.getElementsByTagName('head')[0]).appendChild(s);
  }(window,document,'betterstack','329vFy5k1TVs9ek7wJdhWrLn');
  betterstack('init', { environment: 'production' });
}
