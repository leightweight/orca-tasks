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

// Moves a task on the board when its card is dropped on another column, by
// posting the status the task's page posts. Without this script, a card links
// to its task, where the status menu moves it.
document.querySelectorAll("[data-board]").forEach((board) => {
  let dragged = null;

  document.querySelectorAll(".drag-hint").forEach((hint) => (hint.hidden = false));

  board.addEventListener("dragstart", (event) => {
    dragged = event.target.closest(".task-card");
    dragged?.classList.add("dragging");
  });

  board.addEventListener("dragend", () => {
    dragged?.classList.remove("dragging");
    board.querySelectorAll(".column.drop").forEach((column) => column.classList.remove("drop"));
  });

  board.addEventListener("dragover", (event) => {
    const column = event.target.closest(".column");
    if (!dragged || !column) return;

    event.preventDefault();
    board.querySelectorAll(".column.drop").forEach((other) => other !== column && other.classList.remove("drop"));
    column.classList.add("drop");
  });

  board.addEventListener("drop", async (event) => {
    const column = event.target.closest(".column");
    const card = dragged;
    if (!card || !column || column.contains(card)) return;

    event.preventDefault();

    const from = card.closest(".column");
    column.querySelector("ol").prepend(card);
    recount(from);
    recount(column);

    const response = await fetch(`/browse/${card.dataset.key}/status`, {
      method: "POST",
      headers: { "X-Requested-With": "fetch" },
      body: new URLSearchParams({ status: column.dataset.status }),
    });

    // A move the server refused goes back where it was.
    if (!response.ok) location.reload();
  });
});

function recount(column) {
  column.querySelector(".count").textContent = column.querySelectorAll(".task-card").length;
}
