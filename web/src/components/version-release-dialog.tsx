"use client";

import { useEffect, useState } from "react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import webConfig from "@/constants/common-env";
import { httpRequest } from "@/lib/request";
import type { SystemUpdateInfo } from "@/lib/api";

export function VersionReleaseDialog() {
  const [open, setOpen] = useState(false);
  const [checking, setChecking] = useState(false);
  const [updateInfo, setUpdateInfo] = useState<SystemUpdateInfo | null>(null);

  const checkUpdate = async () => {
    setChecking(true);
    try {
      const result = await httpRequest<SystemUpdateInfo>("/api/system/check-update");
      setUpdateInfo(result);
    } catch {
      // quietly fail
    } finally {
      setChecking(false);
    }
  };

  useEffect(() => {
    if (open && !updateInfo) {
      void checkUpdate();
    }
  }, [open]);

  const hasUpdate = updateInfo?.has_update ?? false;
  const latestVersion = updateInfo?.latest_version ?? "unknown";

  return (
    <>
      <button
        type="button"
        className="relative px-1 py-1 text-[11px] font-medium text-stone-500 transition hover:text-stone-900 dark:text-stone-300 dark:hover:text-white"
        onClick={() => setOpen(true)}
        title="check for updates"
      >
        v{webConfig.appVersion}
        {hasUpdate ? (
          <span className="absolute -top-1 -right-1 size-2 rounded-full bg-emerald-500" />
        ) : null}
      </button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent className="w-[min(94vw,680px)] rounded-2xl">
          <DialogHeader>
            <DialogTitle>Version Update</DialogTitle>
          </DialogHeader>
          <div className="grid grid-cols-2 gap-3">
            <div className="rounded-xl border border-stone-200 bg-white/55 p-3 dark:border-white/10 dark:bg-white/5">
              <div className="text-xs text-stone-500 dark:text-stone-400">
                Current Version
              </div>
              <div className="mt-1 text-base font-semibold text-stone-950 dark:text-stone-100">
                v{webConfig.appVersion}
              </div>
            </div>
            <div className="rounded-xl border border-stone-200 bg-white/55 p-3 dark:border-white/10 dark:bg-white/5">
              <div className="flex items-center justify-between gap-2">
                <div className="text-xs text-stone-500 dark:text-stone-400">
                  Latest Version
                </div>
                <button
                  type="button"
                  className="text-[11px] text-stone-400 underline-offset-2 hover:text-stone-700 hover:underline dark:hover:text-stone-200"
                  onClick={() => void checkUpdate()}
                >
                  {checking ? "Checking..." : "Check"}
                </button>
              </div>
              <div className="mt-1 text-base font-semibold text-stone-950 dark:text-stone-100">
                {latestVersion !== "unknown" ? `v${latestVersion}` : disabledUpdateText(updateInfo)}
              </div>
            </div>
          </div>
          <div className="mt-4 text-xs text-stone-500 dark:text-stone-400">
            {hasUpdate && (
              <p className="text-emerald-600 dark:text-emerald-400 font-medium">
                New version available! Use the button below to update.
              </p>
            )}
            {updateInfo?.warning && (
              <p className="text-amber-600 dark:text-amber-400">{updateInfo.warning}</p>
            )}
            {updateInfo?.build_type && updateInfo.build_type !== "release" && (
              <p>Build type: {updateInfo.build_type} — online update not available. Use Docker or rebuild from source.</p>
            )}
          </div>
          <Button variant="outline" size="sm" asChild>
            <a
              href="https://github.com/ZyphrZero/chatgpt2api"
              target="_blank"
              rel="noreferrer"
            >
              Go to GitHub
            </a>
          </Button>
        </DialogContent>
      </Dialog>
    </>
  );
}

function disabledUpdateText(info: SystemUpdateInfo | null): string {
  if (!info) return "Checking...";
  if (info.build_type && info.build_type !== "release") return "Not available";
  return "Unknown";
}
