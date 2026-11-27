import type { CurrencyEntry } from './currency-types';
import { node, message, id, key as currencyKey, requestID, nonce } from './ledger-data';
import { units, format } from './currency-amount';
import { readPending, savePending, clearPending, definiteRejection } from './currency-pending';
function history(container: HTMLElement, entries: CurrencyEntry[]): void {
  if (!entries.length) { container.replaceChildren(node('p', 'currency-empty', '还没有收支记录。')); return; }
  const scroll = node('div', 'currency-table-scroll'), table = node('table'), head = node('thead'), title = node('tr'), body = node('tbody');
  for (const text of ['交易事项', '变动', '余额']) title.append(node('th', '', text));
  head.append(title); table.append(head, body); scroll.append(table);
  for (const entry of entries) {
    const row = node('tr'), description = node('td');
    description.append(node('strong', '', (entry.label || entry.kind) + (entry.peer ? ` · @${entry.peer}` : '')));
    if (entry.reason) description.append(node('p', '', entry.reason));
    const date = new Date(entry.created_at), meta = node('small'), label = window.ChunnelTime?.formatDateTime(entry.created_at) || '';
    const time = node('time', '', label); if (label && Number.isFinite(date.getTime())) time.dateTime = date.toISOString();
    meta.append(time, document.createTextNode(` · #${entry.id}`)); description.append(meta);
    row.append(description, node('td', entry.incoming ? 'currency-in' : 'currency-out', (entry.incoming ? '+' : '') + entry.delta), node('td', '', entry.balance)); body.append(row);
  }
  container.replaceChildren(scroll);
}
export const currency = Object.freeze({ units, format, nonce, id, requestID, currencyKey, readPending, savePending, clearPending, definiteRejection, history, message });
window.ChunnelCurrency = currency;
