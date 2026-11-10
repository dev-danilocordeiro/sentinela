"use client";

import { useEffect, useState } from "react";
import type { Order, OrderSummary } from "@acme/shared";
import {
  OrderList,
} from "@/components/OrderList";
import { SummaryCard } from "@/components/SummaryCard";
import { fetchSummary, orders } from "@/lib/api";

export default function PedidosPage() {
  const [list, setList] = useState<Order[]>([]);
  const [summary, setSummary] = useState<OrderSummary | null>(null);
  const customer = "c-1";

  useEffect(() => {
    orders.list(customer).then(setList);
    fetchSummary(customer).then(setSummary);
  }, []);

  return (
    <main>
      {summary && <SummaryCard summary={summary} />}
      <OrderList orders={list} onCancel={(id) => orders.cancel(id)} />
    </main>
  );
}
