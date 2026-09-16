import type { ReactNode } from "react";
export function Icon({
  children,
  label,
}: {
  children: ReactNode;
  label?: string;
}) {
  return (
    <span
      className="icon"
      aria-hidden={label ? undefined : true}
      aria-label={label}
    >
      {children}
    </span>
  );
}
export const FolderIcon = () => <Icon>▱</Icon>;
export const FileIcon = () => <Icon>≡</Icon>;
export const CopyIcon = () => <Icon>□</Icon>;
export const LinkIcon = () => <Icon>↗</Icon>;
export const SettingsIcon = () => <Icon>⚙</Icon>;
