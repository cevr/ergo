import { total } from "../app/order";
import { tax } from "../core/price.ts";

export function render(): string {
  return `total ${total(tax)}, tax ${tax}`;
}
