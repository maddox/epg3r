// Everything here is delegated from the document, so markup swapped in by HTMX needs no
// wiring, and it only does what the server cannot: dismissing a panel, and keeping a
// control's own description of itself in step with the clicking.

// The last channel box clicked, for shift-click ranges.
let anchor = null;

document.addEventListener('click', (e) => {
  const el = e.target instanceof Element ? e.target : null;
  if (!el) return;

  // Clear empties a picker and asks for one refresh, rather than making the reader
  // uncheck a dozen boxes one at a time. The panel stays open: they are working in it.
  const clear = el.closest('[data-clear]');
  if (clear) {
    const picker = clear.closest('details.picker');
    picker.querySelectorAll('input:checked').forEach((i) => { i.checked = false; });
    syncPicker(picker);
    picker.dispatchEvent(new Event('change', { bubbles: true }));
    return;
  }

  if (el.closest('[data-clear-selection]')) {
    document.querySelectorAll('input[name="key"]:checked').forEach((b) => { b.checked = false; });
    anchor = null;
    syncSelection();
    return;
  }

  if (el.matches('input[name="key"]')) selectChannels(e, el);

  // A click anywhere else closes an open picker, the way a real select box would.
  document.querySelectorAll('details.picker[open]').forEach((d) => {
    if (!d.contains(el)) d.open = false;
  });
});

document.addEventListener('keydown', (e) => {
  if (e.key !== 'Escape') return;
  document.querySelectorAll('details.picker[open]').forEach((d) => { d.open = false; });
});

// Capture phase: a control that consumes its own change event, so it does not also reach
// the form, would otherwise never reach this either.
document.addEventListener('change', (e) => {
  const picker = e.target.closest && e.target.closest('details.picker');
  if (!picker) return;
  if (e.target.type === 'radio') picker.open = false;
  syncPicker(picker);
}, true);

// What a picker says about itself has to keep up with the clicking. It is answered from
// the inputs rather than from the server, so a choice costs one swap of the list it
// filters and nothing else on the page moves.
function syncPicker(picker) {
  const checked = picker.querySelectorAll('input:checked');
  const n = checked.length;
  const label = picker.querySelector('[data-label]');
  if (label && n && !picker.querySelector('input[type=checkbox]')) {
    label.textContent = checked[0].closest('label').textContent.trim();
  }
  const count = picker.querySelector('[data-count]');
  if (count) { count.textContent = n; count.hidden = n === 0; }
  const chosen = picker.querySelector('[data-chosen]');
  if (chosen) chosen.textContent = n === 0 ? 'None selected' : n + ' selected';
  const clear = picker.querySelector('[data-clear]');
  if (clear) clear.disabled = n === 0;
}

// Shift-click takes everything between the last box clicked and this one, the way a file
// list does, and takes this box's new state so a range can be cleared the way it was set.
// Renumbering a league means selecting dozens of rows.
function selectChannels(e, box) {
  const boxes = Array.from(document.querySelectorAll('input[name="key"]'));
  const to = boxes.indexOf(box);
  const from = boxes.indexOf(anchor); // -1 once the table has been swapped out from under it
  if (e.shiftKey && from !== -1 && from !== to) {
    for (let i = Math.min(from, to); i <= Math.max(from, to); i++) boxes[i].checked = box.checked;
  }
  anchor = box;
  syncSelection();
}

// How many channels are ticked is the reader's own selection, so it is answered here
// rather than by a round trip.
function syncSelection() {
  const bar = document.getElementById('renumber-bar');
  if (!bar) return;
  const n = document.querySelectorAll('input[name="key"]:checked').length;
  bar.querySelector('[data-selected]').textContent = n;
  bar.querySelectorAll('[data-apply], [data-clear-selection]').forEach((b) => { b.disabled = n === 0; });
}

// Shift-clicking a checkbox would otherwise select the text between the two rows.
document.addEventListener('mousedown', (e) => {
  if (e.shiftKey && e.target.matches && e.target.matches('input[name="key"]')) e.preventDefault();
});

// Toasts are raised by the server through an HX-Trigger header.
document.addEventListener('DOMContentLoaded', () => {
  document.body.addEventListener('toast', (e) => {
    const t = e.detail || {};
    const el = document.createElement('div');
    el.className = 'card pointer-events-auto border px-4 py-2 text-sm font-medium toast-' + (t.kind || 'info');
    el.textContent = t.msg || '';
    document.getElementById('toasts').appendChild(el);
    setTimeout(() => { el.remove(); }, 3500);
  });
});
