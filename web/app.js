'use strict';

const $ = (s) => document.querySelector(s);
const grid = $('#grid');
const gridWrap = $('#grid-wrap');
const previewEl = $('#preview');
const sqlEl = $('#sql');
const warningsEl = $('#warnings');
const statusEl = $('#status');
const dimsEl = $('#dims');
const dropEl = $('#drop');
const opt = {
  table: $('#opt-table'),
  header: $('#opt-header'),
  null: $('#opt-null'),
  dialect: $('#opt-dialect'),
  drop: $('#opt-drop'),
  create: $('#opt-create'),
  insert: $('#opt-insert'),
  mode: $('#opt-mode'),
  quote: $('#opt-quote'),
};
const STORE_KEY = 'sqltablegen:draft:v1';
const DIALECTS = { mysql: 'MySQL', postgres: 'PostgreSQL', sqlite: 'SQLite', mssql: 'SQL Server' };

// columns[c] describes column c: { type, length, scale, notNull, pk, unique, ai, default, comment, ref }.
// type '' means "infer from the data". The column name is headers[c]. ref is a foreign key:
// { table, column, onDelete, onUpdate, name }. foreignKeys are multi-column keys from imported
// SQL: { name, columns, table, refColumns, onDelete, onUpdate }; they are kept, not edited.
let state = {
  headers: ['id', 'name', 'email', 'created_at'],
  rows: [
    ['1', 'Alice Smith', 'alice@example.com', '2024-05-01 10:00:00'],
    ['2', 'Bob Jones', 'bob@example.com', '2024-05-02 14:30:00'],
    ['', '', '', ''],
  ],
  columns: [{ type: '', pk: true, ai: true }, { type: '', notNull: true }, { type: '', unique: true }, { type: '' }],
  foreignKeys: [],
};
let previewMode = 'data';
let last = { sql: '', preview: '', structure: '', columns: [] }; // the latest server response

/* ---------------- helpers ---------------- */

function el(tag, props = {}, ...children) {
  const node = document.createElement(tag);
  for (const [k, v] of Object.entries(props)) {
    if (k === 'class') node.className = v;
    else if (k === 'text') node.textContent = v;
    else if (k.startsWith('data-') || k.startsWith('aria-')) node.setAttribute(k, v);
    else node[k] = v;
  }
  node.append(...children);
  return node;
}

function colName(i) {
  let s = '';
  for (i += 1; i > 0; i = Math.floor((i - 1) / 26)) s = String.fromCharCode(65 + ((i - 1) % 26)) + s;
  return s;
}

const ncols = () => state.headers.length;
const lastRow = () => state.rows.length - 1;
const lastCol = () => ncols() - 1;

const plural = (n, word) => `${n} ${word}${n === 1 ? '' : 's'}`;
const sizeText = (rows, cols) => `${plural(rows, 'row')} × ${plural(cols, 'column')}`;
const blankRow = () => Array(ncols()).fill('');
const clamp = (v, lo, hi) => Math.min(Math.max(v, lo), hi);

function normalizeState() {
  const n = Math.max(1, state.headers.length, ...state.rows.map((r) => r.length));
  const fix = (r) => Array.from({ length: n }, (_, i) => (r[i] == null ? '' : String(r[i])));
  state.headers = fix(state.headers);
  state.rows = state.rows.map(fix);
  if (state.rows.length === 0) state.rows.push(blankRow());
  const cols = Array.isArray(state.columns) ? state.columns : [];
  state.columns = Array.from({ length: n }, (_, i) => ({ type: '', ...(cols[i] || {}) }));
  if (!Array.isArray(state.foreignKeys)) state.foreignKeys = [];
}

const sameName = (a, b) => a.trim().toLowerCase() === b.trim().toLowerCase();
// multiKeys returns the multi-column foreign keys column c takes part in.
const multiKeys = (c) => state.foreignKeys.filter((fk) => fk.columns.some((n) => sameName(n, state.headers[c])));
const refText = (table, column) => `${table}(${column || 'id'})`;

function isTableEmpty() {
  return [state.headers, ...state.rows].every((r) => r.every((v) => v.trim() === ''));
}

// Row -1 is the header row.
const getVal = (r, c) => (r < 0 ? state.headers[c] : state.rows[r]?.[c]) ?? '';

function setVal(r, c, v) {
  if (r < 0) state.headers[c] = v;
  else state.rows[r][c] = v;
  const cell = cellAt(r, c);
  if (cell && cell !== editing?.cell) cell.textContent = v;
}

let statusTimer;
function setStatus(msg, kind = '', ms = 5000) {
  statusEl.textContent = msg;
  statusEl.className = 'status ' + kind;
  clearTimeout(statusTimer);
  if (msg && kind !== 'error') statusTimer = setTimeout(() => setStatus(''), ms);
}

async function api(path, body, signal) {
  const isForm = body instanceof FormData;
  const res = await fetch(path, {
    method: 'POST',
    body: isForm ? body : JSON.stringify(body),
    headers: isForm ? undefined : { 'Content-Type': 'application/json' },
    signal,
  });
  const data = await res.json().catch(() => ({ error: res.statusText }));
  if (!res.ok) throw new Error(data.error || res.statusText);
  return data;
}

/* ---------------- undo / redo ---------------- */

const undoStack = [];
const redoStack = [];
const snapshot = () => JSON.stringify(state);

function pushUndo(snap = snapshot()) {
  undoStack.push({ snap, sel: { ...sel } });
  if (undoStack.length > 200) undoStack.shift();
  redoStack.length = 0;
}

// Call before every change of the table: finishes a pending edit and records an undo step.
function beginChange() {
  commitEdit();
  pushUndo();
}

function travel(from, to) {
  commitEdit();
  closePop();
  const step = from.pop();
  if (!step) return;
  to.push({ snap: snapshot(), sel: { ...sel } });
  state = JSON.parse(step.snap);
  build();
  selectRange(step.sel.ar, step.sel.ac, step.sel.r, step.sel.c);
  changed();
}
const undo = () => travel(undoStack, redoStack);
const redo = () => travel(redoStack, undoStack);

/* ---------------- grid rendering ---------------- */

let cellRows = []; // cellRows[r + 1][c] -> <td>, r = -1 is the header row
let chips = []; // chips[c] -> the type button of column c
const cellAt = (r, c) => cellRows[r + 1]?.[c];

function ctlButton(act, title, text, extra = {}) {
  return el('button', { type: 'button', title, text, 'data-act': act, tabIndex: -1, ...extra });
}

function cellTd(r, c, value) {
  return el('td', { class: r < 0 ? 'cell head' : 'cell', text: value, 'data-r': r, 'data-c': c });
}

function build() {
  editing = null;
  normalizeState();
  const n = ncols();
  cellRows = [];

  const colbar = el('tr', { class: 'colbar' }, el('th', { class: 'corner rownum', title: 'Select all' }));
  for (let c = 0; c < n; c++) {
    colbar.append(el('th', { 'data-c': c, title: 'Select column' },
      el('span', { class: 'ctl' },
        el('span', { class: 'label', text: colName(c) }),
        ctlButton('col-ins', 'Insert a column to the right', '+', { 'data-c': c }),
        ctlButton('col-del', 'Delete column', '×', { 'data-c': c, className: 'del' }))));
  }
  colbar.append(el('th', { class: 'addcol' }, ctlButton('col-add', 'Add a column', '+')));

  chips = state.columns.map((_, c) => el('button', {
    type: 'button', class: 'chip', 'data-act': 'col-edit', 'data-c': c, tabIndex: -1,
  }));
  const typeRow = el('tr', { class: 'typerow' },
    el('th', { class: 'rownum', title: 'Column types: click one to set type, key, NOT NULL, default…' },
      el('span', { class: 'label', text: 'type' })),
    ...chips.map((chip) => el('td', {}, chip)),
    el('td', { class: 'addcol' }));

  const headRow = el('tr', { class: 'headrow' },
    el('th', { class: 'rownum', 'data-r': -1, title: 'Select row' },
      el('span', { class: 'ctl' },
        ctlButton('row-ins', 'Insert a row at the top', '+', { 'data-r': -1 }),
        el('span', { class: 'label', text: 'name', title: 'Column names' }))));
  const headCells = state.headers.map((h, c) => cellTd(-1, c, h));
  cellRows.push(headCells);
  headRow.append(...headCells, el('td', { class: 'addcol' }));

  const tbody = el('tbody');
  state.rows.forEach((row, r) => {
    const cells = row.map((v, c) => cellTd(r, c, v));
    cellRows.push(cells);
    tbody.append(el('tr', {},
      el('th', { class: 'rownum', 'data-r': r, title: 'Select row' },
        el('span', { class: 'ctl' },
          ctlButton('row-ins', 'Insert a row below', '+', { 'data-r': r }),
          ctlButton('row-del', 'Delete row', '×', { 'data-r': r, className: 'del' }),
          el('span', { class: 'label', text: r + 1 }))),
      ...cells,
      el('td', { class: 'addcol' })));
  });

  const tfoot = el('tfoot', {},
    el('tr', { class: 'addrow' },
      el('td', { colSpan: n + 2 }, ctlButton('row-add', 'Add a row', '+ row'))));

  grid.replaceChildren(el('thead', {}, colbar, typeRow, headRow), tbody, tfoot);
  dimsEl.textContent = sizeText(state.rows.length, n);
  refreshChips();
  selectRange(sel.ar, sel.ac, sel.r, sel.c, { focus: false, scroll: false });
}

// genericType is how a column type looks before the server has resolved it.
function genericType(col) {
  const t = (col.type || '').toUpperCase();
  if (!t) return 'auto';
  if (col.type === 'decimal' && col.length) return `DECIMAL(${col.length},${col.scale || 0})`;
  if ((col.type === 'varchar' || col.type === 'char') && col.length) return `${t}(${col.length})`;
  return t;
}

// refreshChips redraws the type row: the type from the last server response
// (inferred or set), flags from the current state.
function refreshChips() {
  chips.forEach((chip, c) => {
    const col = state.columns[c] || {};
    const info = last.columns[c];
    const fresh = info && (info.inferred ? !col.type : info.type === col.type);
    let ty;
    let auto = !col.type;
    if (info?.skip) {
      ty = '—';
    } else if (fresh) {
      ty = info.sqlType || genericType(col);
      auto = !!info.inferred;
    } else {
      ty = genericType(col);
    }
    const badges = [];
    const badge = (text, cls, title) => badges.push(el('span', { class: 'badge ' + cls, text, title }));
    if (col.pk) badge('PK', 'pk', 'Primary key');
    if (col.ai) badge('AI', 'ai', 'Auto increment');
    if (col.notNull && !col.pk) badge('NN', '', 'NOT NULL');
    if (col.unique && !col.pk) badge('UQ', '', 'Unique');
    if (col.ref?.table) badge('FK', 'fk', 'References ' + refText(col.ref.table, col.ref.column));
    else if (multiKeys(c).length) badge('FK', 'fk', 'Part of a multi-column foreign key');
    if (col.default) badge('=', '', 'Default: ' + col.default);
    if (col.comment) badge('#', '', 'Comment: ' + col.comment);
    chip.className = 'chip' + (auto ? ' auto' : '') + (info?.skip ? ' skip' : '') + (pop.c === c && !pop.el.hidden ? ' open' : '');
    chip.title = info?.skip ? 'Empty column without a name: left out of the SQL'
      : `${auto ? 'Type inferred from the data' : 'Type'}: ${ty} — click to edit the column`;
    chip.replaceChildren(el('span', { class: 'ty', text: ty }), ...badges);
    cellAt(-1, c)?.classList.toggle('pk', !!col.pk);
  });
}

/* ---------------- selection (Excel-like) ---------------- */

// ar/ac — anchor, r/c — active cell; the selected range spans both.
let sel = { ar: 0, ac: 0, r: 0, c: 0 };
const range = () => ({
  r1: Math.min(sel.ar, sel.r), r2: Math.max(sel.ar, sel.r),
  c1: Math.min(sel.ac, sel.c), c2: Math.max(sel.ac, sel.c),
});

// Hidden textarea that holds focus while cells are selected: it receives
// typing, Ctrl+C / Ctrl+X / Ctrl+V (works on plain http, unlike navigator.clipboard).
const clip = el('textarea', { class: 'clip', 'aria-label': 'Selected cells', spellcheck: false, autocomplete: 'off' });
gridWrap.append(clip);

function focusGrid() {
  if (document.activeElement !== clip) clip.focus({ preventScroll: true });
  clip.value = rangeTSV();
  clip.select();
}

function selectRange(ar, ac, r, c, { focus = true, scroll = true } = {}) {
  const fixR = (v) => clamp(v, -1, lastRow());
  const fixC = (v) => clamp(v, 0, lastCol());
  sel = { ar: fixR(ar), ac: fixC(ac), r: fixR(r), c: fixC(c) };
  paintSelection();
  if (scroll) cellAt(sel.r, sel.c)?.scrollIntoView({ block: 'nearest', inline: 'nearest' });
  if (focus) focusGrid();
}

const select = (r, c, extend = false) =>
  extend ? selectRange(sel.ar, sel.ac, r, c) : selectRange(r, c, r, c);

function paintSelection() {
  grid.querySelectorAll('.sel, .cur, .active').forEach((n) => n.classList.remove('sel', 'cur', 'active'));
  const { r1, r2, c1, c2 } = range();
  const multi = r1 !== r2 || c1 !== c2;
  for (let r = r1; r <= r2; r++) {
    for (let c = c1; c <= c2; c++) {
      const cell = cellAt(r, c);
      if (multi) cell?.classList.add('sel');
    }
    grid.querySelector(`.rownum[data-r="${r}"]`)?.classList.add('active');
  }
  for (let c = c1; c <= c2; c++) grid.querySelector(`.colbar th[data-c="${c}"]`)?.classList.add('active');
  cellAt(sel.r, sel.c)?.classList.add('cur');
}

function rangeValues() {
  const { r1, r2, c1, c2 } = range();
  const out = [];
  for (let r = r1; r <= r2; r++) {
    const row = [];
    for (let c = c1; c <= c2; c++) row.push(getVal(r, c));
    out.push(row);
  }
  return out;
}

// Excel-compatible TSV: quote values that contain tabs, quotes or line breaks.
function rangeTSV() {
  const q = (v) => (/[\t\n\r"]/.test(v) ? `"${v.replace(/"/g, '""')}"` : v);
  return rangeValues().map((row) => row.map(q).join('\t')).join('\n');
}

function clearRange() {
  beginChange();
  const { r1, r2, c1, c2 } = range();
  for (let r = r1; r <= r2; r++) for (let c = c1; c <= c2; c++) setVal(r, c, '');
  changed();
  focusGrid();
}

/* ---------------- edit mode ---------------- */

let editing = null; // { cell, r, c, original, snap }

function caretFromPoint(cell, x, y) {
  let rng = document.caretRangeFromPoint?.(x, y) ?? null;
  if (!rng && document.caretPositionFromPoint) {
    const p = document.caretPositionFromPoint(x, y);
    if (p) { rng = document.createRange(); rng.setStart(p.offsetNode, p.offset); }
  }
  return rng && cell.contains(rng.startContainer) ? rng : null;
}

function placeCaretEnd(node) {
  const rng = document.createRange();
  rng.selectNodeContents(node);
  rng.collapse(false);
  const s = getSelection();
  s.removeAllRanges();
  s.addRange(rng);
}

// startEdit enters text editing in a cell. text replaces the content (typing
// in a selected cell), point puts the caret where the user double-clicked.
function startEdit(r, c, { text = null, point = null } = {}) {
  commitEdit();
  closePop();
  select(r, c);
  const cell = cellAt(sel.r, sel.c);
  if (!cell) return;
  editing = { cell, r: sel.r, c: sel.c, original: getVal(sel.r, sel.c), snap: snapshot() };
  cell.classList.add('editing');
  cell.contentEditable = 'true';
  cell.spellcheck = false;
  if (text !== null) {
    cell.textContent = text;
    setVal(sel.r, sel.c, text);
    changed();
  }
  cell.focus({ preventScroll: true });
  const rng = point && caretFromPoint(cell, point[0], point[1]);
  if (rng) {
    const s = getSelection();
    s.removeAllRanges();
    s.addRange(rng);
  } else {
    placeCaretEnd(cell);
  }
}

function commitEdit() {
  if (!editing) return;
  const { cell, r, c, original, snap } = editing;
  editing = null;
  cell.contentEditable = 'false';
  cell.classList.remove('editing');
  if (getVal(r, c) !== original) {
    undoStack.push({ snap, sel: { ar: r, ac: c, r, c } });
    redoStack.length = 0;
  }
}

function cancelEdit() {
  if (!editing) return;
  const { cell, r, c, original } = editing;
  cell.textContent = original;
  setVal(r, c, original);
  commitEdit();
  changed();
  select(r, c);
}

/* ---------------- structural edits ---------------- */

function insertRow(at) {
  beginChange();
  state.rows.splice(at, 0, blankRow());
  build();
  changed();
}

function insertCol(at) {
  beginChange();
  closePop();
  state.headers.splice(at, 0, '');
  state.rows.forEach((r) => r.splice(at, 0, ''));
  state.columns.splice(at, 0, { type: '' });
  last.columns.splice(at, 0, undefined);
  build();
  changed();
}

function deleteRow(r) {
  beginChange();
  state.rows.splice(r, 1);
  build();
  changed();
}

function deleteCol(c) {
  beginChange();
  closePop();
  if (ncols() === 1) {
    state.headers = [''];
    state.rows = state.rows.map(() => ['']);
    state.columns = [{ type: '' }];
  } else {
    state.headers.splice(c, 1);
    state.rows.forEach((r) => r.splice(c, 1));
    state.columns.splice(c, 1);
    last.columns.splice(c, 1);
  }
  build();
  changed();
}

// Writes a block of values starting at (r0, c0), growing the table if needed.
function pasteBlock(r0, c0, block) {
  beginChange();
  block.forEach((vals, i) => {
    const r = r0 + i;
    let target;
    if (r < 0) {
      target = state.headers;
    } else {
      while (state.rows.length <= r) state.rows.push([]);
      target = state.rows[r];
    }
    vals.forEach((v, j) => { target[c0 + j] = v; });
  });
  const w = Math.max(...block.map((b) => b.length));
  build();
  selectRange(r0, c0, r0 + block.length - 1, c0 + w - 1);
  changed();
  setStatus(`Pasted ${block.length} × ${w}`, 'ok');
}

const FORMATS = {
  csv: 'CSV', tsv: 'TSV', mysql: 'MySQL output', pipe: 'pipe-separated', vertical: 'MySQL \\G',
  sql: 'SQL', describe: 'DESCRIBE',
};

function loadTable(res, how) {
  beginChange();
  closePop();
  state = { headers: res.headers || [], rows: res.rows || [], columns: res.columns || [], foreignKeys: res.foreignKeys || [] };
  last.columns = [];
  if (res.tableName) opt.table.value = res.tableName;
  sel = { ar: -1, ac: 0, r: -1, c: 0 };
  build();
  changed();
  let msg = `${how} (${FORMATS[res.format] || res.format})`;
  if (res.format === 'sql') {
    msg = `${how}: ${res.tableName || 'table'}` + (res.dialect ? ` (${DIALECTS[res.dialect]})` : '');
  }
  msg += res.format === 'describe'
    ? `: ${plural(res.headers.length, 'column')}, structure only`
    : `: ${sizeText(res.rows.length, res.headers.length)}`;
  if (res.notes?.length) msg += ' · ' + res.notes.join(' · ');
  setStatus(msg, 'ok', res.notes?.length ? 12000 : 5000);
}

/* ---------------- generate (Go backend) ---------------- */

let renderTimer;
let renderAbort;

function changed() {
  clearTimeout(renderTimer);
  renderTimer = setTimeout(() => { save(); generate(); }, 80);
}

function sqlOptions() {
  return {
    dialect: opt.dialect.value,
    table: opt.table.value.trim(),
    create: opt.create.checked,
    drop: opt.drop.checked,
    insert: opt.insert.checked,
    insertMode: opt.mode.value,
    quoteAll: opt.quote.checked,
    emptyAsNull: opt.null.checked,
  };
}

async function generate() {
  renderAbort?.abort();
  renderAbort = new AbortController();
  try {
    const data = await api('/api/generate', {
      table: { headers: state.headers, rows: state.rows },
      columns: state.columns,
      foreignKeys: state.foreignKeys,
      options: sqlOptions(),
    }, renderAbort.signal);
    last = { sql: data.sql, preview: data.preview, structure: data.structure, columns: data.columns || [] };
    showPreview();
    showSQL(data.warnings || []);
    refreshChips();
    if (pop.c >= 0 && !pop.el.hidden) showPopType();
    if (statusEl.classList.contains('error')) setStatus('');
  } catch (err) {
    if (err.name !== 'AbortError') setStatus('Generating failed: ' + err.message, 'error');
  }
}

const EMPTY_HINT = 'The table is empty — fill in the cells, paste data or SQL (Ctrl+V), or drop a CSV / SQL file';

function showText(pre, text, empty) {
  pre.classList.toggle('empty', !text);
  pre.textContent = text || empty;
}

function showPreview() {
  const text = previewMode === 'data' ? last.preview : last.structure;
  showText(previewEl, text, EMPTY_HINT);
}

function showSQL(warnings) {
  if (!last.sql) {
    const nothing = !opt.create.checked && !opt.insert.checked && !opt.drop.checked;
    showText(sqlEl, '', nothing ? 'Tick CREATE or INSERT to get SQL' : EMPTY_HINT);
  } else {
    sqlEl.classList.remove('empty');
    highlight(sqlEl, last.sql);
  }
  warningsEl.hidden = warnings.length === 0;
  warningsEl.replaceChildren(...warnings.map((w) => el('li', { text: w })));
}

const KEYWORDS = new Set(`CREATE TABLE DROP IF EXISTS NOT NULL DEFAULT PRIMARY KEY UNIQUE AUTO_INCREMENT
  AUTOINCREMENT IDENTITY GENERATED BY AS INSERT INTO VALUES SET ON OFF COMMENT IS COLUMN ENGINE CHARSET
  SELECT FROM TRUE FALSE CURRENT_TIMESTAMP CURRENT_DATE CAST IDENTITY_INSERT CONSTRAINT FOREIGN
  REFERENCES CASCADE RESTRICT NO ACTION DELETE UPDATE PRAGMA`.split(/\s+/));
const TYPES = new Set(`INT INTEGER BIGINT SMALLINT DECIMAL NUMERIC DOUBLE PRECISION REAL FLOAT BOOLEAN BIT
  VARCHAR NVARCHAR CHAR NCHAR TEXT DATE TIME DATETIME DATETIME2 TIMESTAMP JSON JSONB UUID
  UNIQUEIDENTIFIER BLOB BYTEA VARBINARY MAX`.split(/\s+/));
const TOKEN_RE = /(--[^\n]*)|(N?'(?:[^'\\]|''|\\.)*')|(`[^`\n]*`|"[^"\n]*"|\[[^\]\n]*\])|(\b\d+(?:\.\d+)?\b)|([A-Za-z_][A-Za-z0-9_]*)/g;

// highlight puts SQL into pre with token colors; text nodes only, no HTML parsing.
function highlight(pre, sql) {
  if (sql.length > 300000) { pre.textContent = sql; return; } // too big to be worth it
  const frag = document.createDocumentFragment();
  let pos = 0;
  for (const m of sql.matchAll(TOKEN_RE)) {
    if (m.index > pos) frag.append(sql.slice(pos, m.index));
    let cls = '';
    if (m[1]) cls = 'c';
    else if (m[2]) cls = 's';
    else if (m[3]) cls = 'q';
    else if (m[4]) cls = 'n';
    else if (KEYWORDS.has(m[5].toUpperCase())) cls = 'k';
    else if (TYPES.has(m[5].toUpperCase())) cls = 't';
    frag.append(cls ? el('span', { class: 'hl-' + cls, text: m[0] }) : m[0]);
    pos = m.index + m[0].length;
  }
  frag.append(sql.slice(pos));
  pre.replaceChildren(frag);
}

/* ---------------- column popover ---------------- */

const pop = {
  el: $('#colpop'),
  c: -1,
  undone: false, // an undo step was recorded for this popover session
  f: {
    name: $('#pc-name'), type: $('#pc-type'), len: $('#pc-len'), scale: $('#pc-scale'),
    pk: $('#pc-pk'), ai: $('#pc-ai'), nn: $('#pc-nn'), uq: $('#pc-uq'),
    def: $('#pc-def'), comment: $('#pc-comment'),
    fk: $('#pc-fk'), fkTable: $('#pc-fk-table'), fkCol: $('#pc-fk-col'), fkDel: $('#pc-fk-del'), fkUpd: $('#pc-fk-upd'),
  },
};

function openPop(c) {
  commitEdit();
  if (pop.c === c && !pop.el.hidden) { closePop(true); return; }
  pop.c = c;
  pop.undone = false;
  const col = state.columns[c];
  const f = pop.f;
  $('#pop-title').textContent = `Column ${colName(c)}`;
  f.name.value = state.headers[c];
  f.type.value = col.type || '';
  f.len.value = col.length || '';
  f.scale.value = col.scale || '';
  f.pk.checked = !!col.pk;
  f.ai.checked = !!col.ai;
  f.nn.checked = !!col.notNull;
  f.uq.checked = !!col.unique;
  f.def.value = col.default || '';
  f.comment.value = col.comment || '';
  const ref = col.ref || {};
  f.fk.checked = !!ref.table;
  f.fkTable.value = ref.table || '';
  f.fkCol.value = ref.column || '';
  f.fkDel.value = ref.onDelete || '';
  f.fkUpd.value = ref.onUpdate || '';
  showPopType();
  pop.el.hidden = false;
  placePop();
  refreshChips();
  f.type.focus();
}

function placePop() {
  const chip = chips[pop.c];
  if (!chip) return;
  const r = chip.getBoundingClientRect();
  const w = pop.el.offsetWidth;
  const left = clamp(r.left, 8, document.documentElement.clientWidth - w - 8);
  pop.el.style.left = `${left + scrollX}px`;
  pop.el.style.top = `${r.bottom + scrollY + 6}px`;
}

function closePop(focus = false) {
  if (pop.el.hidden) return;
  pop.el.hidden = true;
  const c = pop.c;
  pop.c = -1;
  refreshChips();
  if (focus && c >= 0) select(-1, c);
}

// showPopType shows the fields that make sense for the type and the type
// the column gets in the chosen dialect.
function showPopType() {
  const f = pop.f;
  const t = f.type.value;
  const sized = t === 'varchar' || t === 'char' || t === 'decimal';
  $('#pc-len-wrap').hidden = !sized;
  $('#pc-scale-wrap').hidden = t !== 'decimal';
  $('#pc-len-label').textContent = t === 'decimal' ? 'Precision' : 'Length';
  $('#pc-fk-body').hidden = !f.fk.checked;
  const multi = pop.c >= 0 ? multiKeys(pop.c) : [];
  $('#pc-fk-multi').hidden = multi.length === 0;
  $('#pc-fk-multi').textContent = multi.map((fk) =>
    `Part of FOREIGN KEY (${fk.columns.join(', ')}) → ${fk.table}(${(fk.refColumns || []).join(', ')}), kept from the imported SQL`).join('\n');
  const info = last.columns[pop.c];
  const dialect = DIALECTS[opt.dialect.value];
  $('#pc-sqltype').textContent = info?.sqlType && !info.skip
    ? `${dialect}: ${info.sqlType}${info.inferred ? ' (inferred)' : ''}` : '';
}

function popChanged(e) {
  const c = pop.c;
  if (c < 0) return;
  if (!pop.undone) { beginChange(); pop.undone = true; }
  const f = pop.f;
  const num = (input) => Math.max(0, parseInt(input.value, 10) || 0);
  // An auto-increment column is normally the primary key (MySQL even requires a key).
  if (e?.target === f.ai && f.ai.checked && !f.pk.checked && !f.uq.checked &&
      !state.columns.some((col, i) => i !== c && col.pk)) f.pk.checked = true;
  // Ticking "Foreign key" guesses the table from the name: customer_id → customers.
  if (e?.target === f.fk && f.fk.checked && !f.fkTable.value) {
    const m = f.name.value.trim().match(/^(.+?)(?:_id|Id|ID)$/);
    if (m) f.fkTable.value = m[1].endsWith('s') ? m[1] : m[1] + 's';
    f.fkTable.focus();
  }
  const prevRef = state.columns[c].ref;
  state.columns[c] = {
    type: f.type.value,
    length: num(f.len),
    scale: num(f.scale),
    pk: f.pk.checked,
    ai: f.ai.checked,
    notNull: f.nn.checked,
    unique: f.uq.checked,
    default: f.def.value,
    comment: f.comment.value,
  };
  if (f.fk.checked) {
    state.columns[c].ref = {
      name: prevRef?.name || '', // a constraint name from imported SQL is kept
      table: f.fkTable.value.trim(),
      column: f.fkCol.value.trim(),
      onDelete: f.fkDel.value,
      onUpdate: f.fkUpd.value,
    };
  }
  if (state.headers[c] !== f.name.value) {
    setVal(-1, c, f.name.value);
  }
  showPopType();
  refreshChips();
  changed();
}

pop.el.addEventListener('input', popChanged);
pop.el.addEventListener('change', popChanged);
pop.el.addEventListener('keydown', (e) => {
  if (e.key === 'Escape') { e.preventDefault(); closePop(true); }
  if (e.key === 'Enter' && e.target.tagName === 'INPUT' && e.target.type !== 'checkbox') {
    e.preventDefault();
    closePop(true);
  }
});
document.addEventListener('mousedown', (e) => {
  if (!pop.el.hidden && !pop.el.contains(e.target) && !e.target.closest('.chip')) closePop();
});
gridWrap.addEventListener('scroll', () => { if (!pop.el.hidden) placePop(); });
window.addEventListener('resize', () => { if (!pop.el.hidden) placePop(); });

/* ---------------- import & clipboard ---------------- */

// Something that is an SQL script rather than data; the server has the final word.
// The prefix skips comments, blank lines and empty statements ("/*!40101 ... */;").
const SQL_HINT = /^(?:\s|(?:--|#)[^\n]*(?:\n|$)|\/\*[\s\S]*?\*\/|;)*(?=(?:create|insert|replace|copy|alter|drop|set|use|begin|start|lock|pragma|go|comment|if)\b)[\s\S]*?\b(?:create\s+(?:\w+\s+){0,3}table\s+[^(;]{1,300}\(|(?:insert|replace)\s+[^;]{1,5000}?\bvalues?\s*\(|copy\s+[^;]{1,5000}?\bfrom\s+stdin\b)/i;
const looksLikeSQL = (text) => SQL_HINT.test(text);

async function parseText(text, format = 'auto') {
  try {
    return await api('/api/parse', { text, format, header: opt.header.checked });
  } catch (err) {
    setStatus('Cannot read the data: ' + err.message, 'error');
    return null;
  }
}

async function uploadFile(file) {
  const fd = new FormData();
  fd.append('file', file);
  fd.append('format', 'auto');
  fd.append('header', String(opt.header.checked));
  try {
    loadTable(await api('/api/upload', fd), `File “${file.name}”`);
  } catch (err) {
    setStatus(`File “${file.name}”: ${err.message}`, 'error');
  }
}

// pasteIntoSelection is Ctrl+V on selected cells.
async function pasteIntoSelection(text) {
  const body = text.replace(/\r?\n$/, '');
  if (!body) return;
  if (looksLikeSQL(body)) {
    const res = await parseText(text);
    if (res?.format === 'sql') { loadTable(res, 'Pasted SQL'); return; }
  }
  const { r1, r2, c1, c2 } = range();

  // A plain value (no tabs, line breaks or '|'): like Excel, fill the whole selection.
  if (!/[\t\n|]/.test(body)) {
    beginChange();
    for (let r = r1; r <= r2; r++) for (let c = c1; c <= c2; c++) setVal(r, c, body.replace(/\r/g, ''));
    changed();
    focusGrid();
    return;
  }

  const res = await parseText(text);
  if (!res) return;
  const atOrigin = r1 === -1 && c1 === 0 && r1 === r2 && c1 === c2;
  const fullTable = (res.hasHeader && ['mysql', 'pipe', 'vertical'].includes(res.format)) ||
    res.format === 'describe' || res.format === 'sql';
  if (fullTable || (atOrigin && isTableEmpty())) {
    loadTable(res, 'Pasted');
    return;
  }
  // Everything that was copied, header line included, goes in from the top-left selected cell.
  pasteBlock(r1, c1, res.hasHeader ? [res.headers, ...res.rows] : res.rows);
}

document.addEventListener('paste', async (e) => {
  const target = e.target instanceof Element ? e.target : e.target?.parentElement;
  const text = e.clipboardData?.getData('text/plain') ?? '';

  if (editing && target === editing.cell) {
    e.preventDefault();
    const body = text.replace(/\r?\n$/, '');
    if (/[\t\n]/.test(body)) {
      const { r, c } = editing;
      commitEdit();
      select(r, c);
      await pasteIntoSelection(text);
    } else {
      document.execCommand('insertText', false, body.replace(/\r/g, ''));
    }
    return;
  }
  if (target === clip) {
    e.preventDefault();
    await pasteIntoSelection(text);
    return;
  }
  if (target?.closest('input, textarea, select, [contenteditable="true"], dialog, .pop')) return;

  // Outside the grid: SQL or a multi-line paste replaces the whole table.
  const body = text.replace(/\r?\n$/, '');
  if (!/[\n\t]/.test(body) && !looksLikeSQL(body)) return;
  e.preventDefault();
  const res = await parseText(text);
  if (res) loadTable(res, 'Pasted');
});

clip.addEventListener('copy', (e) => {
  e.preventDefault();
  e.clipboardData.setData('text/plain', rangeTSV());
  setStatus('Copied ' + sizeText(range().r2 - range().r1 + 1, range().c2 - range().c1 + 1), 'ok');
});

clip.addEventListener('cut', (e) => {
  e.preventDefault();
  e.clipboardData.setData('text/plain', rangeTSV());
  clearRange();
});

/* ---------------- SQL import dialog ---------------- */

const dlg = $('#sql-dlg');
const dlgInput = $('#sql-in');
const dlgError = $('#sql-dlg-error');

function openImport() {
  commitEdit();
  closePop();
  dlgError.textContent = '';
  dlg.showModal();
  dlgInput.focus();
  dlgInput.select();
}

async function runImport() {
  const text = dlgInput.value;
  if (!text.trim()) { dlgError.textContent = 'Paste an SQL script first'; return; }
  try {
    const res = await api('/api/parse', { text, format: looksLikeSQL(text) ? 'sql' : 'auto', header: true });
    dlg.close();
    loadTable(res, 'Imported');
  } catch (err) {
    dlgError.textContent = err.message;
  }
}

dlgInput.addEventListener('keydown', (e) => {
  if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) { e.preventDefault(); runImport(); }
});
dlg.addEventListener('close', () => focusGrid());

/* ---------------- keyboard ---------------- */

function moveAfterEdit(r, c, dr, dc) {
  commitEdit();
  if (dc > 0 && c >= lastCol()) { r += 1; c = 0; dc = 0; }
  if (dc < 0 && c <= 0) { if (r > -1) { r -= 1; c = lastCol() + 1; } }
  const nr = r + dr;
  if (nr > lastRow()) insertRow(lastRow() + 1);
  select(nr, c + dc);
}

// Keys while a cell is being edited.
grid.addEventListener('keydown', (e) => {
  if (!editing || e.target !== editing.cell || e.isComposing) return;
  const { r, c } = editing;
  switch (e.key) {
    case 'Enter':
      e.preventDefault();
      moveAfterEdit(r, c, e.shiftKey ? -1 : 1, 0);
      break;
    case 'Tab':
      e.preventDefault();
      moveAfterEdit(r, c, 0, e.shiftKey ? -1 : 1);
      break;
    case 'ArrowUp':
    case 'ArrowDown':
      e.preventDefault();
      commitEdit();
      select(r + (e.key === 'ArrowUp' ? -1 : 1), c);
      break;
    case 'Escape':
      e.preventDefault();
      cancelEdit();
      break;
  }
});

// Keys while cells are selected (focus is on the hidden textarea).
clip.addEventListener('keydown', (e) => {
  if (e.isComposing || e.key === 'Process') return;
  const mod = e.ctrlKey || e.metaKey;
  const { r, c } = sel;

  if (mod && !e.altKey) {
    // e.code keeps shortcuts working on non-Latin keyboard layouts.
    switch (e.code) {
      case 'KeyZ': e.preventDefault(); (e.shiftKey ? redo : undo)(); return;
      case 'KeyY': e.preventDefault(); redo(); return;
      case 'KeyA': e.preventDefault(); selectRange(-1, 0, lastRow(), lastCol()); return;
      case 'KeyC': case 'KeyX': case 'KeyV': case 'Insert': return; // native copy/cut/paste events
    }
  }

  const arrows = { ArrowUp: [-1, 0], ArrowDown: [1, 0], ArrowLeft: [0, -1], ArrowRight: [0, 1] };
  if (arrows[e.key]) {
    e.preventDefault();
    const [dr, dc] = arrows[e.key];
    let nr = r + dr;
    let nc = c + dc;
    if (mod) { // jump to the edge, like Excel's Ctrl+arrow
      if (dr) nr = dr < 0 ? -1 : lastRow();
      if (dc) nc = dc < 0 ? 0 : lastCol();
    }
    select(nr, nc, e.shiftKey);
    return;
  }

  switch (e.key) {
    case 'Enter':
      e.preventDefault();
      select(r + (e.shiftKey ? -1 : 1), c);
      return;
    case 'Tab':
      e.preventDefault();
      if (e.shiftKey) select(c > 0 ? r : r - 1, c > 0 ? c - 1 : (r > -1 ? lastCol() : 0));
      else select(c < lastCol() ? r : Math.min(r + 1, lastRow()), c < lastCol() ? c + 1 : (r < lastRow() ? 0 : c));
      return;
    case 'Home':
      e.preventDefault();
      if (mod) select(-1, 0, e.shiftKey); else select(r, 0, e.shiftKey);
      return;
    case 'End':
      e.preventDefault();
      if (mod) select(lastRow(), lastCol(), e.shiftKey); else select(r, lastCol(), e.shiftKey);
      return;
    case 'F2':
      e.preventDefault();
      startEdit(r, c);
      return;
    case 'F4': // column settings of the current column
      e.preventDefault();
      openPop(c);
      return;
    case 'Delete':
    case 'Backspace':
      e.preventDefault();
      clearRange();
      return;
    case 'Escape':
      select(r, c);
      return;
  }

  // A printable key starts editing and replaces the cell content, as in Excel.
  const altGr = e.getModifierState?.('AltGraph');
  if (e.key.length === 1 && (!mod || altGr) && !(e.altKey && !altGr)) {
    e.preventDefault();
    startEdit(r, c, { text: e.key });
  }
});

// Undo and redo work wherever the focus is — after a click on the SQL, a
// checkbox or an empty spot — except in text fields, where the browser undoes
// the typing itself.
document.addEventListener('keydown', (e) => {
  if (e.defaultPrevented || e.isComposing || e.altKey || !(e.ctrlKey || e.metaKey)) return;
  if (e.code !== 'KeyZ' && e.code !== 'KeyY') return;
  const t = e.target;
  const typing = t.isContentEditable || t.tagName === 'TEXTAREA' ||
    (t.tagName === 'INPUT' && /^(text|search|number|email|url|tel|password)$/.test(t.type));
  if (t === clip || typing || dlg.open) return;
  e.preventDefault();
  (e.code === 'KeyY' || e.shiftKey ? redo : undo)();
});

// IME input (e.g. CJK) and virtual keyboards that do not report key values.
clip.addEventListener('compositionend', (e) => {
  if (e.data) startEdit(sel.r, sel.c, { text: e.data });
});
clip.addEventListener('input', (e) => {
  if (e.isComposing) return;
  if (e.inputType === 'insertText' && e.data) startEdit(sel.r, sel.c, { text: e.data });
  else focusGrid();
});

/* ---------------- mouse ---------------- */

let dragging = false;
const posOf = (cell) => [+cell.dataset.r, +cell.dataset.c];

grid.addEventListener('mousedown', (e) => {
  if (e.button !== 0 || e.target.closest('button')) return;
  const cell = e.target.closest('td.cell');
  if (cell) {
    if (editing?.cell === cell) return; // move the caret inside the edited cell
    e.preventDefault();
    commitEdit();
    const [r, c] = posOf(cell);
    select(r, c, e.shiftKey);
    dragging = true;
    return;
  }
  const th = e.target.closest('th');
  if (!th || th.classList.contains('addcol')) return;
  e.preventDefault();
  commitEdit();
  if (th.classList.contains('corner')) selectRange(-1, 0, lastRow(), lastCol());
  else if (th.dataset.c !== undefined) selectRange(-1, +th.dataset.c, lastRow(), +th.dataset.c);
  else if (th.dataset.r !== undefined) selectRange(+th.dataset.r, 0, +th.dataset.r, lastCol());
});

grid.addEventListener('mouseover', (e) => {
  if (!dragging) return;
  const cell = e.target.closest('td.cell');
  if (cell) {
    const [r, c] = posOf(cell);
    if (r !== sel.r || c !== sel.c) selectRange(sel.ar, sel.ac, r, c, { scroll: false });
  }
});

window.addEventListener('mouseup', () => { dragging = false; });

grid.addEventListener('dblclick', (e) => {
  const cell = e.target.closest('td.cell');
  if (!cell || editing?.cell === cell) return;
  const [r, c] = posOf(cell);
  startEdit(r, c, { point: [e.clientX, e.clientY] });
});

grid.addEventListener('input', (e) => {
  if (!editing || e.target !== editing.cell) return;
  const v = editing.cell.textContent.replace(/\n/g, ' ');
  if (editing.r < 0) state.headers[editing.c] = v;
  else state.rows[editing.r][editing.c] = v;
  changed();
});

grid.addEventListener('beforeinput', (e) => {
  // keep cells plain text: no bold/italic, no line breaks, no rich drops
  if (e.inputType.startsWith('format') || e.inputType === 'insertParagraph' ||
      e.inputType === 'insertLineBreak' || e.inputType === 'insertFromDrop') {
    e.preventDefault();
  }
});

// Clicking anywhere else finishes the edit.
grid.addEventListener('focusout', (e) => {
  if (editing && e.target === editing.cell) commitEdit();
});

/* ---------------- buttons ---------------- */

async function copyText(text, btn) {
  if (!text) return;
  try {
    await navigator.clipboard.writeText(text);
  } catch {
    const ta = el('textarea', { value: text });
    document.body.append(ta);
    ta.select();
    document.execCommand('copy');
    ta.remove();
  }
  btn.textContent = 'Copied ✓';
  setTimeout(() => { btn.textContent = 'Copy'; }, 1500);
}

function download(text, name, type) {
  if (!text) return;
  const url = URL.createObjectURL(new Blob([text], { type: `${type};charset=utf-8` }));
  el('a', { href: url, download: name }).click();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}

const fileBase = () => (opt.table.value.trim() || 'table').replace(/[^\w.-]+/g, '_');

function setPreviewMode(mode) {
  previewMode = mode;
  document.querySelectorAll('[data-act^="pv-"][aria-pressed]').forEach((b) => {
    b.setAttribute('aria-pressed', String(b.dataset.act === (mode === 'data' ? 'pv-data' : 'pv-struct')));
  });
  showPreview();
  save();
}

document.addEventListener('click', (e) => {
  const btn = e.target.closest('[data-act]');
  if (!btn) return;
  const r = +btn.dataset.r;
  const c = +btn.dataset.c;
  switch (btn.dataset.act) {
    case 'row-add': insertRow(state.rows.length); select(lastRow(), 0); break;
    case 'col-add': insertCol(ncols()); select(-1, lastCol()); break;
    case 'row-ins': insertRow(r + 1); select(r + 1, 0); break;
    case 'col-ins': insertCol(c + 1); select(-1, c + 1); break;
    case 'row-del': deleteRow(r); select(Math.min(r, lastRow()), sel.c); break;
    case 'col-del': deleteCol(c); select(sel.r, Math.min(c, lastCol())); break;
    case 'col-edit': openPop(c); break;
    case 'pop-close': closePop(true); break;
    case 'pop-reset':
      pop.f.type.value = '';
      for (const k of ['len', 'scale', 'def', 'comment']) pop.f[k].value = '';
      for (const k of ['pk', 'ai', 'nn', 'uq', 'fk']) pop.f[k].checked = false;
      for (const k of ['fkTable', 'fkCol', 'fkDel', 'fkUpd']) pop.f[k].value = '';
      popChanged();
      break;
    case 'clear':
      if (!isTableEmpty()) {
        beginChange();
        closePop();
        state = { headers: ['', '', ''], rows: [['', '', ''], ['', '', ''], ['', '', '']], columns: [], foreignKeys: [] };
        last.columns = [];
        build();
        changed();
        setStatus('Table cleared — Ctrl+Z to undo', 'ok');
      }
      select(-1, 0);
      break;
    case 'sql-import': openImport(); break;
    case 'sql-import-run': runImport(); break;
    case 'pv-data': setPreviewMode('data'); break;
    case 'pv-struct': setPreviewMode('structure'); break;
    case 'pv-copy': copyText(previewMode === 'data' ? last.preview : last.structure, btn); break;
    case 'pv-download':
      download(previewMode === 'data' ? last.preview : last.structure, `${fileBase()}.txt`, 'text/plain');
      break;
    case 'sql-copy': copyText(last.sql, btn); break;
    case 'sql-download': download(last.sql, `${fileBase()}.sql`, 'application/sql'); break;
  }
});

$('#file').addEventListener('change', (e) => {
  const f = e.target.files[0];
  if (f) uploadFile(f);
  e.target.value = '';
});

for (const input of Object.values(opt)) input.addEventListener(input === opt.table ? 'input' : 'change', changed);

/* ---------------- drag & drop ---------------- */

const hasFiles = (e) => Array.from(e.dataTransfer?.types || []).includes('Files');
let dragDepth = 0;

window.addEventListener('dragenter', (e) => {
  if (!hasFiles(e)) return;
  e.preventDefault();
  dragDepth++;
  dropEl.hidden = false;
});
window.addEventListener('dragleave', (e) => {
  if (!hasFiles(e)) return;
  if (--dragDepth <= 0) { dragDepth = 0; dropEl.hidden = true; }
});
window.addEventListener('dragover', (e) => {
  if (!hasFiles(e)) return;
  e.preventDefault();
  e.dataTransfer.dropEffect = 'copy';
});
window.addEventListener('drop', (e) => {
  if (!hasFiles(e)) return;
  e.preventDefault();
  dragDepth = 0;
  dropEl.hidden = true;
  const f = e.dataTransfer.files[0];
  if (f) uploadFile(f);
});

/* ---------------- splitter between the preview and the SQL ---------------- */

const outputsEl = $('.outputs');
const splitter = $('#splitter');
const SPLIT_DEFAULT = 0.4;
let split = SPLIT_DEFAULT; // the preview's share of the width; kept as a share, so it survives window resizes

function setSplit(v, persist = false) {
  const w = outputsEl.clientWidth || 1;
  const gap = splitter.offsetWidth;
  const min = Math.min(0.45, 280 / w); // keep both panels usable
  split = clamp(v, min, Math.max(min, 1 - (280 + gap) / w));
  outputsEl.style.setProperty('--split', `${(split * 100).toFixed(2)}%`);
  splitter.setAttribute('aria-valuenow', String(Math.round(split * 100)));
  if (persist) save();
}

splitter.addEventListener('pointerdown', (e) => {
  if (e.button !== 0) return;
  e.preventDefault();
  splitter.setPointerCapture(e.pointerId);
  splitter.classList.add('dragging');
  document.body.classList.add('resizing');
});
splitter.addEventListener('pointermove', (e) => {
  if (!splitter.hasPointerCapture(e.pointerId)) return;
  const r = outputsEl.getBoundingClientRect();
  setSplit((e.clientX - r.left - splitter.offsetWidth / 2) / r.width);
});
const endDrag = () => {
  if (!splitter.classList.contains('dragging')) return;
  splitter.classList.remove('dragging');
  document.body.classList.remove('resizing');
  save();
};
splitter.addEventListener('pointerup', endDrag);
splitter.addEventListener('lostpointercapture', endDrag);
splitter.addEventListener('dblclick', () => setSplit(SPLIT_DEFAULT, true));
splitter.addEventListener('keydown', (e) => {
  const step = e.shiftKey ? 0.1 : 0.02;
  const to = { ArrowLeft: split - step, ArrowRight: split + step, Home: 0, End: 1 }[e.key];
  if (to === undefined) return;
  e.preventDefault();
  setSplit(to, true);
});
window.addEventListener('resize', () => setSplit(split));

/* ---------------- persistence (current draft only) ---------------- */

const CHECKS = ['header', 'null', 'drop', 'create', 'insert', 'quote'];
const SELECTS = ['dialect', 'mode'];

function save() {
  try {
    const opts = { table: opt.table.value, preview: previewMode, split };
    for (const k of CHECKS) opts[k] = opt[k].checked;
    for (const k of SELECTS) opts[k] = opt[k].value;
    localStorage.setItem(STORE_KEY, JSON.stringify({ table: state, opts }));
  } catch { /* storage unavailable or full — not critical */ }
}

function restore() {
  let o = {};
  try {
    const saved = JSON.parse(localStorage.getItem(STORE_KEY) || 'null');
    if (saved?.table?.headers && saved.table.rows) state = saved.table;
    o = saved?.opts || {};
  } catch { /* ignore a broken draft */ }
  opt.table.value = typeof o.table === 'string' ? o.table : 'users';
  for (const k of CHECKS) if (typeof o[k] === 'boolean') opt[k].checked = o[k];
  for (const k of SELECTS) {
    if (typeof o[k] === 'string' && [...opt[k].options].some((x) => x.value === o[k])) opt[k].value = o[k];
  }
  if (o.preview === 'structure') previewMode = 'structure';
  if (typeof o.split === 'number' && o.split > 0 && o.split < 1) split = o.split;
}

restore();
setSplit(split);
build();
setPreviewMode(previewMode);
generate();
