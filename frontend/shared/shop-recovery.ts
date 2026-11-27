import { shopPending } from './shop-data';
import type { ShopPending } from './shop-types';

/** An uncertain request is immutable until its matching receipt has been verified. */
export function createShopRecovery(storage: Pick<Storage, 'getItem' | 'setItem' | 'removeItem'>) {
  const key = (owner: string) => `newpress.shop.pending.${owner}`;
  function read(owner: string): ShopPending | null {
    if (!owner) return null;
    try {
      const raw = storage.getItem(key(owner));
      return raw === null ? null : shopPending(JSON.parse(raw) as unknown, owner);
    } catch { throw new Error('上次下单恢复记录无法读取，请先在我的订单中核对；当前不会创建新订单。'); }
  }
  function remember(value: ShopPending): ShopPending {
    const parsed = shopPending(value, value.owner), previous = read(value.owner);
    if (previous) {
      if (JSON.stringify(previous.draft) !== JSON.stringify(parsed.draft)) throw new Error('上次下单仍待核对，请先恢复原请求。');
      return previous;
    }
    try { storage.setItem(key(value.owner), JSON.stringify(parsed)); }
    catch { throw new Error('浏览器无法保存下单恢复信息，请允许会话存储后重试。'); }
    return parsed;
  }
  function forget(value: ShopPending): boolean {
    const previous = read(value.owner);
    if (!previous) return true;
    if (JSON.stringify(previous.draft) !== JSON.stringify(value.draft)) return false;
    storage.removeItem(key(value.owner)); return true;
  }
  return { read, remember, forget };
}
