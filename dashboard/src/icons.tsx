import type { ReactNode } from "react";
export function Icon({
  children,
  label,
}: {
  children: ReactNode;
  label?: string;
}) {
  return (
    <svg
      className="icon"
      viewBox="0 0 24 24"
      width="18"
      height="18"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.8"
      strokeLinecap="square"
      strokeLinejoin="miter"
      aria-hidden={label ? undefined : true}
      aria-label={label}
      role={label ? "img" : undefined}
    >
      {children}
    </svg>
  );
}
export const FolderIcon = () => (
  <Icon>
    <path d="M3 6h7l2 3h9v11H3Z" />
  </Icon>
);
export const FileIcon = () => (
  <Icon>
    <path d="M5 3h9l5 5v13H5Z M14 3v6h5 M9 13h6 M9 17h6" />
  </Icon>
);
export const CopyIcon = () => (
  <Icon>
    <path d="M8 8h13v13H8Z M16 8V3H3v13h5" />
  </Icon>
);
export const LinkIcon = () => (
  <Icon>
    <path d="M9 8H7a4 4 0 0 0 0 8h2 M15 8h2a4 4 0 0 1 0 8h-2 M8 12h8" />
  </Icon>
);
export const OverviewIcon = () => (
  <Icon>
    <path d="M3 3h7v7H3Z M14 3h7v7h-7Z M3 14h7v7H3Z M14 14h7v7h-7Z" />
  </Icon>
);
export const ChartIcon = () => (
  <Icon>
    <path d="M4 3v18h17 M8 17v-5 M13 17V7 M18 17V4" />
  </Icon>
);
export const SearchIcon = () => (
  <Icon>
    <circle cx="10" cy="10" r="6" />
    <path d="m15 15 6 6" />
  </Icon>
);
export const CodeIcon = () => (
  <Icon>
    <path d="m7 7-5 5 5 5 M17 7l5 5-5 5 M14 4l-4 16" />
  </Icon>
);
export const ShieldIcon = () => (
  <Icon>
    <path d="m12 3 8 3v6c0 5-8 9-8 9s-8-4-8-9V6Z" />
  </Icon>
);
export const SettingsIcon = () => (
  <Icon>
    <path d="M4 7h16 M4 17h16 M8 4v6 M16 14v6" />
  </Icon>
);
export const MenuIcon = () => (
  <Icon>
    <path d="M4 6h16 M4 12h16 M4 18h16" />
  </Icon>
);
export const CloseIcon = () => (
  <Icon>
    <path d="m6 6 12 12 M18 6 6 18" />
  </Icon>
);
export const RefreshIcon = () => (
  <Icon>
    <path d="M20 5v6h-6 M20 11a8 8 0 1 0 0 5" />
  </Icon>
);
export const InfoIcon = () => (
  <Icon>
    <circle cx="12" cy="12" r="9" />
    <path d="M12 11v6 M12 7v1" />
  </Icon>
);
export const SunIcon = () => (
  <Icon>
    <circle cx="12" cy="12" r="4" />
    <path d="M12 2v2 M12 20v2 M2 12h2 M20 12h2 M5 5l1.5 1.5 M17.5 17.5 19 19 M5 19l1.5-1.5 M17.5 6.5 19 5" />
  </Icon>
);
export const MoonIcon = () => (
  <Icon>
    <path d="M20 15.5A9 9 0 0 1 8.5 4 9 9 0 1 0 20 15.5Z" />
  </Icon>
);
export const ChevronIcon = ({ open = false }: { open?: boolean }) => (
  <Icon>
    <path d={open ? "m6 9 6 6 6-6" : "m9 6 6 6-6 6"} />
  </Icon>
);
export const UpIcon = () => (
  <Icon>
    <path d="M12 20V4 M5 11l7-7 7 7" />
  </Icon>
);
