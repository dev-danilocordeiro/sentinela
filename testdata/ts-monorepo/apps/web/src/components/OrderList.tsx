import type { Order } from "@acme/shared";
import { formatCents } from "@/lib/money";

export function OrderList({ orders, onCancel }: { orders: Order[]; onCancel: (id: string) => void }) {
  return (
    <ul>
      {orders.map((o) => (
        <li key={o.id}>
          {o.id} · {formatCents(o.total)}
          {o.status === "open" && <button onClick={() => onCancel(o.id)}>Cancelar</button>}
        </li>
      ))}
    </ul>
  );
}
