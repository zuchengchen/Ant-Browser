import { Suspense, useCallback, useEffect, useRef, useState } from "react";
import type { KeyboardEvent as ReactKeyboardEvent } from "react";
import { createBrowserRouter, RouterProvider } from "react-router-dom";
import { ThemeProvider } from "./shared/theme";
import { Layout } from "./shared/layout";
import { Alert, MODAL_EXIT_DURATION_MS, ToastContainer, Modal, Button, Loading, toast } from "./shared/components";
import { AlertCircle, X } from "lucide-react";
import { AppRoutes } from "./routes/AppRoutes";
import { lazyNamed } from "./routes/lazyNamed";
import { useNotificationStore } from "./store/notificationStore";
import { useBackupStore } from "./store/backupStore";
import { waitForAppReady } from "./utils/appReady";
import { installWailsOperationLogger } from "./utils/wailsOperationLogger";
import {
  ForceQuit as ForceQuitApp,
  GetDatabaseRecoveryNotice,
  QuitAppOnly as QuitAppOnlyApp,
} from "./wailsjs/go/main/App";
import {
  Environment,
  Quit,
  WindowHide,
} from "./wailsjs/runtime/runtime";

const QuickLaunchModal = lazyNamed(
  () => import("./modules/browser/components/QuickLaunchModal"),
  "QuickLaunchModal",
);

function useWailsNotifications() {
  const addNotification = useNotificationStore((s) => s.addNotification);

  useEffect(() => {
    const runtime = (window as any).runtime;
    if (!runtime?.EventsOn) return;

    const offCrashed = runtime.EventsOn(
      "browser:instance:crashed",
      (data: { profileId: string; profileName: string; error: string }) => {
        const action = data.profileId
          ? {
              type: "navigate" as const,
              label: "查看实例",
              path: `/browser/detail/${encodeURIComponent(data.profileId)}`,
            }
          : undefined;
        addNotification({
          type: "error",
          title: "实例异常退出",
          message: `「${data.profileName || data.profileId}」意外崩溃：${data.error}`,
          source: "runtime",
          dedupeKey: `browser:instance:crashed:${data.profileId}`,
          persistent: true,
          action,
        });
      },
    );

    const offBridgeFailed = runtime.EventsOn(
      "proxy:bridge:failed",
      (data: { profileId: string; profileName: string; error: string }) => {
        const action = data.profileId
          ? {
              type: "navigate" as const,
              label: "查看实例",
              path: `/browser/detail/${encodeURIComponent(data.profileId)}`,
            }
          : undefined;
        toast.warning(`「${data.profileName || data.profileId}」代理桥接失败：${data.error}`, 8_000, {
          title: "代理桥接失败",
          source: "runtime",
          dedupeKey: `proxy:bridge:failed:${data.profileId}`,
          persistent: true,
          action,
        });
      },
    );

    const offBridgeDied = runtime.EventsOn(
      "proxy:bridge:died",
      (data: { key: string; error: string; proxyIds?: string[]; profileIds?: string[] }) => {
        const proxyIds = data.proxyIds?.filter(Boolean) || [];
        const profileIds = data.profileIds?.filter(Boolean) || [];
        const target = proxyIds.length > 0 ? proxyIds.join("、") : data.key;
        const profileHint = profileIds.length > 0 ? `，影响实例 ${profileIds.join("、")}` : "";
        addNotification({
          type: "warning",
          title: "连接池节点失效",
          message: `代理节点 ${target} 连接中断${profileHint}，相关实例可能无法访问网络`,
          source: "runtime",
          dedupeKey: `proxy:bridge:died:${data.key}`,
          persistent: true,
          action: {
            type: "navigate",
            label: "查看代理池",
            path: "/browser/proxy-pool",
          },
        });
      },
    );

    return () => {
      offCrashed?.();
      offBridgeFailed?.();
      offBridgeDied?.();
    };
  }, [addNotification]);
}

function useGlobalErrorNotifications() {
  const addNotification = useNotificationStore((s) => s.addNotification);

  useEffect(() => {
    const toMessage = (value: unknown) => {
      if (value instanceof Error) return value.message || String(value);
      if (typeof value === "string") return value;
      try {
        return JSON.stringify(value);
      } catch {
        return String(value);
      }
    };

    const handleError = (event: ErrorEvent) => {
      const message = event.message || toMessage(event.error) || "未知脚本错误";
      addNotification({
        type: "error",
        title: "前端异常",
        message,
        source: "frontend",
        dedupeKey: `frontend:error:${message.slice(0, 160)}`,
        persistent: true,
      });
    };

    const handleUnhandledRejection = (event: PromiseRejectionEvent) => {
      const message = toMessage(event.reason) || "未知 Promise 异常";
      addNotification({
        type: "error",
        title: "未处理异步异常",
        message,
        source: "frontend",
        dedupeKey: `frontend:rejection:${message.slice(0, 160)}`,
        persistent: true,
      });
    };

    window.addEventListener("error", handleError);
    window.addEventListener("unhandledrejection", handleUnhandledRejection);
    return () => {
      window.removeEventListener("error", handleError);
      window.removeEventListener("unhandledrejection", handleUnhandledRejection);
    };
  }, [addNotification]);
}

function DatabaseRecoveryNotice() {
  const [notice, setNotice] = useState<Record<string, string> | null>(null);

  useEffect(() => {
    const app = (window as Window & { go?: { main?: { App?: Record<string, unknown> } } }).go?.main?.App;
    if (typeof app?.GetDatabaseRecoveryNotice !== "function") return;

    let disposed = false;
    void waitForAppReady()
      .then(() => disposed ? null : GetDatabaseRecoveryNotice())
      .then((recovery) => {
        if (disposed || !recovery) return;
        const { reason, backupPath, parkedPath } = recovery;
        if (!reason && !backupPath && !parkedPath) return;

        setNotice(recovery);
        useNotificationStore.getState().addNotification({
          type: "warning",
          title: "数据库已重建",
          message: [
            "旧数据库已损坏，历史实例、代理和插件记录未恢复。",
            reason && `原因：${reason}`,
            backupPath && `损坏库备份：${backupPath}`,
            parkedPath && `原库保留位置：${parkedPath}`,
          ].filter(Boolean).join("\n"),
          source: "system",
          dedupeKey: `system:database-recovery:${backupPath || parkedPath}`,
        });
      })
      .catch((error: unknown) => console.warn("读取数据库重建通知失败", error));

    return () => { disposed = true; };
  }, []);

  if (!notice) return null;

  return (
    <div role="alert">
      <Alert
        type="warning"
        title="数据库已重建"
        className="mb-4"
        message={(
          <div className="space-y-2">
            <p>旧数据库已损坏，历史实例、代理和插件记录未恢复。</p>
            <details className="space-y-1">
              <summary className="cursor-pointer">查看备份位置和重建原因</summary>
              {notice.backupPath && <p className="break-all">损坏库备份：{notice.backupPath}</p>}
              {notice.parkedPath && <p className="break-all">原库保留位置：{notice.parkedPath}</p>}
              {notice.reason && <p className="break-words">原因：{notice.reason}</p>}
            </details>
            <Button variant="secondary" size="sm" onClick={() => setNotice(null)}>知道了</Button>
          </div>
        )}
      />
    </div>
  );
}

type CloseAction = "tray" | "app-only" | "app-and-browser";
type ClosePhase = "idle" | "confirm" | CloseAction;

function WindowCloseControl() {
  const [phase, setPhase] = useState<ClosePhase>("idle");
  const phaseRef = useRef<ClosePhase>("idle");
  const [platform, setPlatform] = useState("");
  const closeActionTimerRef = useRef<number | null>(null);
  const closeButtonRef = useRef<HTMLButtonElement>(null);
  const choicesRef = useRef<HTMLDivElement>(null);
  const returnFocusRef = useRef<HTMLElement | null>(null);
  const importInProgress = useBackupStore((s) => s.importInProgress);
  const importProgress = useBackupStore((s) => s.importProgress);
  const importMessage = useBackupStore((s) => s.importMessage);
  const supportsTray = platform === "windows";
  const quitting = phase === "app-only" || phase === "app-and-browser";

  // Native and DOM events can arrive before React renders a pending choice.
  const changePhase = useCallback((next: ClosePhase) => {
    phaseRef.current = next;
    setPhase(next);
  }, []);

  const requestClose = useCallback(() => {
    if (phaseRef.current !== "idle") return;
    const focused = document.activeElement;
    returnFocusRef.current = focused instanceof HTMLElement && focused !== document.body ? focused : closeButtonRef.current;
    changePhase("confirm");
  }, [changePhase]);

  useEffect(() => () => {
    if (closeActionTimerRef.current !== null) {
      window.clearTimeout(closeActionTimerRef.current);
    }
  }, []);

  useEffect(() => {
    const runtime = (window as any).runtime;
    if (!runtime?.EventsOn) return;

    const off = runtime.EventsOn("app:request-close", requestClose);
    return () => {
      if (typeof off === "function") off();
    };
  }, [requestClose]);

  useEffect(() => {
    let cancelled = false;

    if (!(window as any).runtime?.Environment) return;
    Environment()
      .then((info) => {
        if (!cancelled && info?.platform) {
          setPlatform(info.platform);
        }
      })
      .catch(() => {});

    return () => {
      cancelled = true;
    };
  }, []);

  const closeModal = () => {
    if (phaseRef.current !== "confirm") return;
    changePhase("idle");
    requestAnimationFrame(() => {
      if (phaseRef.current !== "idle") return;
      const previous = returnFocusRef.current;
      (previous?.isConnected ? previous : closeButtonRef.current)?.focus();
    });
  };

  useEffect(() => {
    // A new request can arrive while the shared modal is still animating out.
    if (phase === "confirm") choicesRef.current?.querySelector("button")?.focus();
  }, [phase]);

  const handleDialogKey = (event: ReactKeyboardEvent<HTMLDivElement>) => {
    if (phaseRef.current !== "confirm") return;
    if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "k") {
      event.preventDefault();
      event.stopPropagation();
      return;
    }
    if (event.key === "Escape") {
      event.preventDefault();
      event.stopPropagation();
      closeModal();
      return;
    }
    if (event.key !== "Tab") return;
    const buttons = event.currentTarget.querySelectorAll<HTMLButtonElement>("button:not(:disabled)");
    const first = buttons[0];
    const last = buttons[buttons.length - 1];
    if (document.activeElement === (event.shiftKey ? first : last)) {
      event.preventDefault();
      (event.shiftKey ? last : first)?.focus();
    }
  };

  const confirmAction = (action: CloseAction) => {
    if (phaseRef.current !== "confirm") return;
    changePhase(action);
    closeActionTimerRef.current = window.setTimeout(async () => {
      closeActionTimerRef.current = null;
      try {
        if (action === "tray") {
          WindowHide();
          changePhase("idle");
        } else if (action === "app-only") {
          await QuitAppOnlyApp();
        } else {
          let fallbackTimer: number | undefined;
          try {
            await Promise.race([
              ForceQuitApp(),
              new Promise<void>((resolve) => {
                fallbackTimer = window.setTimeout(resolve, 1200);
              }),
            ]);
          } catch (error) {
            console.error("ForceQuit failed, falling back to runtime.Quit()", error);
          } finally {
            window.clearTimeout(fallbackTimer);
          }
          Quit();
        }
      } catch (error) {
        console.error("Window close action failed", error);
        changePhase("idle");
      }
    }, MODAL_EXIT_DURATION_MS);
  };

  const confirmation = (
    <Modal
      open={phase === "confirm"}
      onClose={closeModal}
      ariaLabel="关闭窗口确认"
      closeButtonLabel="取消关闭"
      onKeyDown={handleDialogKey}
      title={importInProgress ? "关闭应用确认" : undefined}
      width={importInProgress ? "360px" : "420px"}
      closable={!quitting}
    >
      <div className="flex flex-col items-center pt-2 pb-6 px-4">
        <div
          className={`w-12 h-12 rounded-full flex items-center justify-center mb-4 ${
            importInProgress
              ? "bg-amber-50 text-amber-500"
              : "bg-red-50 text-red-500"
          }`}
        >
          <AlertCircle className="w-6 h-6" />
        </div>
        {importInProgress && (
          <h3 className="text-lg font-medium text-[var(--color-text-primary)] mb-2">
            正在导入备份，是否关闭？
          </h3>
        )}
        {importInProgress ? (
          <p className="text-sm text-[var(--color-text-secondary)] text-center mb-6">
            当前正在导入备份
            {importProgress > 0 ? `（${importProgress}%）` : ""}。
            <br />
            {importMessage || "强制关闭会中断本次导入，是否仍要关闭应用？"}
          </p>
        ) : (
          <p className="mb-6 text-sm text-center text-[var(--color-text-secondary)]">
            可仅退出应用，或连同浏览器一起关闭。
          </p>
        )}

        <div
          ref={choicesRef}
          className={`w-full ${importInProgress ? "flex gap-3" : "flex flex-col gap-2"}`}
        >
          {importInProgress ? (
            <>
              <Button
                variant="secondary"
                className="flex-1"
                onClick={closeModal}
                autoFocus
                disabled={quitting}
              >
                继续加载
              </Button>
              <Button
                variant="danger"
                className="flex-1"
                onClick={() => confirmAction("app-and-browser")}
                loading={phase === "app-and-browser"}
              >
                仍要关闭
              </Button>
            </>
          ) : (
            <>
              <Button
                variant="secondary"
                className="w-full"
                onClick={supportsTray ? () => confirmAction("tray") : closeModal}
                autoFocus
                disabled={quitting}
              >
                {supportsTray ? "最小化到托盘" : "取消"}
              </Button>
              <Button
                className="w-full"
                onClick={() => confirmAction("app-only")}
                loading={phase === "app-only"}
                disabled={quitting}
              >
                仅退出应用
              </Button>
              <Button
                variant="danger"
                className="w-full"
                onClick={() => confirmAction("app-and-browser")}
                loading={phase === "app-and-browser"}
                disabled={quitting}
              >
                退出应用与浏览器
              </Button>
            </>
          )}
        </div>
      </div>
    </Modal>
  );

  return (
    <>
      <button
        ref={closeButtonRef}
        type="button"
        title="关闭窗口"
        aria-label="关闭窗口"
        disabled={phase !== "idle"}
        onClick={requestClose}
        className="inline-flex h-7 w-9 shrink-0 items-center justify-center rounded text-[var(--color-text-secondary)] transition-colors hover:bg-[var(--color-error)] hover:text-white focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)]"
      >
        <X className="h-4 w-4" aria-hidden="true" />
      </button>
      {confirmation}
    </>
  );
}

function AppLayout() {
  useEffect(() => {
    installWailsOperationLogger();
  }, []);
  useWailsNotifications();
  useGlobalErrorNotifications();
  const [quickLaunchOpen, setQuickLaunchOpen] = useState(false);
  const routeFallback = (
    <div className="flex min-h-[240px] items-center justify-center py-10">
      <Loading text="页面加载中..." />
    </div>
  );

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.isComposing) return;
      if (!(event.ctrlKey || event.metaKey)) return;
      if (event.key.toLowerCase() !== "k") return;
      event.preventDefault();
      setQuickLaunchOpen((prev) => !prev);
    };

    window.addEventListener("keydown", onKeyDown);
    return () => {
      window.removeEventListener("keydown", onKeyDown);
    };
  }, []);

  return (
    <>
      <Layout closeControl={<WindowCloseControl />}>
        <DatabaseRecoveryNotice />
        <Suspense fallback={routeFallback}>
          <AppRoutes />
        </Suspense>
      </Layout>
      <ToastContainer />
      <Suspense fallback={null}>
        {quickLaunchOpen ? (
          <QuickLaunchModal
            open={quickLaunchOpen}
            onClose={() => setQuickLaunchOpen(false)}
          />
        ) : null}
      </Suspense>
    </>
  );
}

const router = createBrowserRouter([{ path: "*", element: <AppLayout /> }]);

function App() {
  return (
    <ThemeProvider>
      <RouterProvider router={router} />
    </ThemeProvider>
  );
}

export default App;
