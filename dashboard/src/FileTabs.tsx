import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { CloseIcon } from "./icons";

const name = (path: string) => path.split("/").at(-1);

export function FileTabs({
  paths,
  active,
  onOpen,
  onClose,
}: {
  paths: string[];
  active: string;
  onOpen: (path: string) => void;
  onClose: (paths: string[]) => void;
}) {
  const tabs = useRef<HTMLDivElement>(null);
  const menuRef = useRef<HTMLDivElement>(null);
  const [menu, setMenu] = useState<{
    path: string;
    x: number;
    y: number;
    trigger: HTMLButtonElement;
  } | null>(null);

  const dismiss = () => {
    setMenu(null);
    requestAnimationFrame(() => {
      const target = menu?.trigger.isConnected
        ? menu.trigger
        : tabs.current?.querySelector<HTMLButtonElement>(
            '[aria-selected="true"]',
          ) || tabs.current;
      target?.focus();
    });
  };

  useLayoutEffect(() => {
    if (!menu || !menuRef.current) return;
    const element = menuRef.current;
    const { width, height } = element.getBoundingClientRect();
    element.style.left = `${Math.max(8, Math.min(menu.x, window.innerWidth - width - 8))}px`;
    element.style.top = `${Math.max(8, Math.min(menu.y, window.innerHeight - height - 8))}px`;
    element.querySelector<HTMLButtonElement>("button:not(:disabled)")?.focus();
  }, [menu]);

  useEffect(() => {
    if (!menu) return;
    const outside = (event: PointerEvent) => {
      if (!menuRef.current?.contains(event.target as Node)) setMenu(null);
    };
    const hide = () => setMenu(null);
    document.addEventListener("pointerdown", outside);
    window.addEventListener("resize", hide);
    window.addEventListener("blur", hide);
    document.addEventListener("scroll", hide, true);
    return () => {
      document.removeEventListener("pointerdown", outside);
      window.removeEventListener("resize", hide);
      window.removeEventListener("blur", hide);
      document.removeEventListener("scroll", hide, true);
    };
  }, [menu]);

  const index = menu ? paths.indexOf(menu.path) : -1;
  const actions = menu
    ? [
        { label: "Close", paths: [menu.path] },
        { label: "Close Others", paths: paths.filter((p) => p !== menu.path) },
        { label: "Close to the Right", paths: paths.slice(index + 1) },
        { label: "Close to the Left", paths: paths.slice(0, index) },
        { label: "Close All", paths },
      ]
    : [];

  return (
    <>
      <div
        className="file-tabs"
        role="tablist"
        aria-label="Open files"
        ref={tabs}
        tabIndex={-1}
      >
        {paths.map((path) => (
          <div
            className={path === active ? "active" : ""}
            key={path}
            onContextMenu={(event) => {
              event.preventDefault();
              const trigger = event.currentTarget.querySelector("button")!;
              const rect = trigger.getBoundingClientRect();
              setMenu({
                path,
                x: event.clientX || rect.left,
                y: event.clientY || rect.bottom,
                trigger,
              });
            }}
            onKeyDown={(event) => {
              if (
                event.key !== "ContextMenu" &&
                !(event.shiftKey && event.key === "F10")
              )
                return;
              event.preventDefault();
              const trigger = event.currentTarget.querySelector("button")!;
              const rect = trigger.getBoundingClientRect();
              setMenu({ path, x: rect.left, y: rect.bottom, trigger });
            }}
          >
            <button
              role="tab"
              aria-selected={path === active}
              onClick={() => onOpen(path)}
            >
              {name(path)}
            </button>
            <button
              aria-label={`Close ${name(path)}`}
              onClick={() => onClose([path])}
            >
              <CloseIcon />
            </button>
          </div>
        ))}
      </div>
      {menu && (
        <div
          className="tab-context-menu"
          role="menu"
          aria-label={`Tab actions for ${name(menu.path)}`}
          ref={menuRef}
          onContextMenu={(event) => event.preventDefault()}
          onKeyDown={(event) => {
            if (event.key === "Escape" || event.key === "Tab") {
              event.preventDefault();
              dismiss();
              return;
            }
            if (!["ArrowDown", "ArrowUp", "Home", "End"].includes(event.key))
              return;
            event.preventDefault();
            const items = Array.from(
              event.currentTarget.querySelectorAll<HTMLButtonElement>(
                "button:not(:disabled)",
              ),
            );
            const current = items.indexOf(
              document.activeElement as HTMLButtonElement,
            );
            const next =
              event.key === "Home"
                ? 0
                : event.key === "End"
                  ? items.length - 1
                  : (current +
                      (event.key === "ArrowDown" ? 1 : -1) +
                      items.length) %
                    items.length;
            items[next]?.focus();
          }}
        >
          {actions.map((action) => (
            <button
              key={action.label}
              role="menuitem"
              tabIndex={-1}
              disabled={!action.paths.length}
              onClick={() => {
                onClose(action.paths);
                dismiss();
              }}
            >
              {action.label}
            </button>
          ))}
        </div>
      )}
    </>
  );
}
