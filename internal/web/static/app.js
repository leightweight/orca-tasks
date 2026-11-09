// Previews Markdown in a form. A button with data-preview="ID" swaps the
// textarea with that ID for its rendered text, from the server's /preview, and
// back. The buttons start hidden, so without this script they never show.
document.querySelectorAll("[data-preview]").forEach((button) => (button.hidden = false));

document.addEventListener("click", async (event) => {
  const button = event.target.closest("[data-preview]");
  if (!button) return;

  const field = document.getElementById(button.dataset.preview);
  const preview = document.getElementById(button.dataset.preview + "-preview");
  const showing = button.getAttribute("aria-pressed") === "true";

  if (!showing) {
    // goldmark drops raw HTML, so the server's rendering is safe to insert.
    const response = await fetch("/preview", { method: "POST", body: new URLSearchParams({ text: field.value }) });
    preview.innerHTML = response.ok ? await response.text() : "<p>The preview failed. Your text is still there.</p>";
  }

  field.hidden = !showing;
  preview.hidden = showing;
  button.setAttribute("aria-pressed", String(!showing));
  button.textContent = showing ? "Preview" : "Write";
});
