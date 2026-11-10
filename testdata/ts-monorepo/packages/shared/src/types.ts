export type OrderStatus = "open" | "paid" | "cancelled";

export interface Order {
  id: string;
  customerId: string;
  status: OrderStatus;
  /** Valor total em centavos. */
  total: number;
  createdAt: string;
}

export interface OrderSummary {
  count: number;
  totalCents: number;
}
