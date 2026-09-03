// Pickers are <details> dressed as select boxes, so the dismissing a real select does
// for free is done here: choose on a single-select and it closes, click elsewhere or
// press Escape and any of them closes. Everything is delegated from the document, so
// markup swapped in by HTMX needs no wiring.
document.addEventListener('click', function (e) {
  var clear = e.target.closest && e.target.closest('[data-clear]');
  if (clear) {
    // Clear empties the panel and asks for one refresh, rather than making the reader
    // uncheck a dozen boxes one at a time.
    var picker = clear.closest('details.picker');
    picker.querySelectorAll('input:checked').forEach(function (i) { i.checked = false; });
    syncPicker(picker);
    picker.dispatchEvent(new Event('change', { bubbles: true }));
    return;
  }
  document.querySelectorAll('details.picker[open]').forEach(function (d) {
    if (!d.contains(e.target)) d.open = false;
  });
});

document.addEventListener('keydown', function (e) {
  if (e.key !== 'Escape') return;
  document.querySelectorAll('details.picker[open]').forEach(function (d) { d.open = false; });
});

// What a picker says about itself has to keep up with the clicking. It is answered from
// the inputs rather than from the server, so a choice costs one swap of the list it
// filters and nothing else on the page moves.
function syncPicker(picker) {
  var checked = picker.querySelectorAll('input:checked');
  var n = checked.length;
  var label = picker.querySelector('[data-label]');
  if (label && n && !picker.querySelector('input[type=checkbox]')) {
    label.textContent = checked[0].closest('label').textContent.trim();
  }
  var count = picker.querySelector('[data-count]');
  if (count) { count.textContent = n; count.hidden = n === 0; }
  var chosen = picker.querySelector('[data-chosen]');
  if (chosen) chosen.textContent = n === 0 ? 'None selected' : n + ' selected';
  var clear = picker.querySelector('[data-clear]');
  if (clear) clear.disabled = n === 0;
}

// Capture phase: a control that consumes its own change event, so it does not also reach
// the form, would otherwise never reach this either.
document.addEventListener('change', function (e) {
  var picker = e.target.closest && e.target.closest('details.picker');
  if (!picker) return;
  if (e.target.type === 'radio') picker.open = false;
  syncPicker(picker);
}, true);

// Toasts are raised by the server through an HX-Trigger header.
document.addEventListener('DOMContentLoaded', function () {
  document.body.addEventListener('toast', function (e) {
    var t = e.detail || {};
    var el = document.createElement('div');
    el.className = 'card pointer-events-auto border px-4 py-2 text-sm font-medium toast-' + (t.kind || 'info');
    el.textContent = t.msg || '';
    document.getElementById('toasts').appendChild(el);
    setTimeout(function () { el.remove(); }, 3500);
  });
});
