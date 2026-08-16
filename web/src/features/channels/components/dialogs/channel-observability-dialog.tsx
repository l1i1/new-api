import { RefreshCw } from "lucide-react";
/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.
*/
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";

import { StaticDataTable } from "@/components/data-table";
import { Dialog } from "@/components/dialog";
import { Button } from "@/components/ui/button";

import { getChannelObservability } from "../../api";
import type { ChannelObservabilityResult } from "../../types";
import { useChannels } from "../channels-provider";

type ChannelObservabilityDialogProps = {
  open: boolean;
  onOpenChange: (open: boolean) => void;
};

function formatPercent(value: number): string {
  return `${value.toFixed(1)}%`;
}

function formatSampled(
  value: number,
  sufficient: boolean,
  translate: (key: string) => string,
): string {
  return sufficient ? formatPercent(value) : translate("Insufficient sample");
}

function formatRangeLabel(
  value: number,
  translate: (key: string) => string,
): string {
  if (value === 1) return translate("1 hour");
  if (value === 24) return translate("24 hours");
  return translate("7 days");
}

export function ChannelObservabilityDialog(
  props: ChannelObservabilityDialogProps,
) {
  const { t } = useTranslation();
  const { currentRow } = useChannels();
  const [hours, setHours] = useState(24);
  const [rows, setRows] = useState<ChannelObservabilityResult[]>([]);
  const [isLoading, setIsLoading] = useState(false);

  const load = async () => {
    if (!currentRow) return;
    setIsLoading(true);
    try {
      const response = await getChannelObservability(currentRow.id, hours);
      if (!response.success) {
        toast.error(response.message || t("Operation failed"));
        return;
      }
      setRows(response.data?.items || []);
    } catch (error: unknown) {
      toast.error(
        error instanceof Error ? error.message : t("Operation failed"),
      );
    } finally {
      setIsLoading(false);
    }
  };

  useEffect(() => {
    if (props.open) void load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [props.open, currentRow?.id, hours]);

  return (
    <Dialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title={t("Channel observability")}
      description={t(
        "Requests, retries, cache behavior, p95 latency, and time to first token by model",
      )}
      contentClassName="max-w-6xl"
      contentHeight="min(80vh, 760px)"
      bodyClassName="space-y-4"
    >
      <div className="flex items-center justify-between gap-3">
        <div className="flex gap-2">
          {[1, 24, 168].map((value) => (
            <Button
              key={value}
              variant={hours === value ? "default" : "outline"}
              size="sm"
              onClick={() => setHours(value)}
            >
              {formatRangeLabel(value, t)}
            </Button>
          ))}
        </div>
        <Button
          variant="outline"
          size="icon-sm"
          onClick={() => void load()}
          disabled={isLoading}
          aria-label={t("Refresh")}
        >
          <RefreshCw className={isLoading ? "size-4 animate-spin" : "size-4"} />
        </Button>
      </div>
      <div className="min-h-0 overflow-auto rounded-md border">
        {rows.length === 0 && !isLoading ? (
          <div className="text-muted-foreground py-12 text-center">
            {t("No observability data available")}
          </div>
        ) : (
          <StaticDataTable
            data={rows}
            getRowKey={(row) =>
              `${row.requested_model}-${row.group}-${row.protocol}-${row.credential_id}`
            }
            tableClassName="min-w-[1180px]"
            columns={[
              {
                id: "model",
                header: t("Model"),
                className: "min-w-[180px]",
                cell: (row) => row.requested_model,
              },
              {
                id: "requests",
                header: t("Requests"),
                cell: (row) => row.request_count.toLocaleString(),
              },
              {
                id: "attempts",
                header: t("Attempts"),
                cell: (row) => row.attempt_count.toLocaleString(),
              },
              {
                id: "success",
                header: t("Success rate"),
                cell: (row) =>
                  formatSampled(
                    row.request_success_rate,
                    row.sample_sufficient,
                    t,
                  ),
              },
              {
                id: "attempt-success",
                header: t("Attempt success"),
                cell: (row) =>
                  formatSampled(
                    row.attempt_success_rate,
                    row.sample_sufficient,
                    t,
                  ),
              },
              {
                id: "cache",
                header: t("Cache hit rate"),
                cell: (row) =>
                  formatSampled(row.cache_hit_rate, row.usage_sufficient, t),
              },
              {
                id: "latency",
                header: t("P95 latency"),
                cell: (row) =>
                  row.sample_sufficient
                    ? `${row.p95_latency_ms} ms`
                    : t("Insufficient sample"),
              },
              {
                id: "ttft",
                header: t("P95 first token"),
                cell: (row) =>
                  row.sample_sufficient
                    ? `${row.p95_ttft_ms} ms`
                    : t("Insufficient sample"),
              },
              {
                id: "coverage",
                header: t("Coverage"),
                cell: (row) => formatPercent(row.sample_coverage),
              },
            ]}
          />
        )}
      </div>
    </Dialog>
  );
}
