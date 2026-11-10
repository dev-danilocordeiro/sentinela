import type { OrderSummary } from "@acme/shared/types";
import { formatCents } from "../lib/money";

export function SummaryCard({ summary }: { summary: OrderSummary }) {
  return (
    <p>
      {summary.count} pedidos · {formatCents(summary.totalCents)}
    </p>
  );
}
