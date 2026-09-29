document.addEventListener("click", async (event) => {
  const button = event.target instanceof Element ? event.target.closest("[data-copy-id]") : null;
  if (!(button instanceof HTMLButtonElement)) return;
  const identity = button.dataset.copyId || "";
  const status = document.querySelector(".copy-status");
  try {
    await navigator.clipboard.writeText(identity);
    button.textContent = "Copied";
    if (status) status.textContent = `${identity} copied to the clipboard.`;
  } catch {
    if (status) status.textContent = `Copy this ID: ${identity}`;
  }
});
