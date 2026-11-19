// The price of one item, and the tax on it.
export const tax = 2;

export function priceOf(n: number): number {
  return n * 3 + tax;
}

function unused(n: number): number {
  return priceOf(n) - tax;
}
