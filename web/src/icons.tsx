import type { SVGProps } from "react";
import type { Role } from "./types";

type IconProps = SVGProps<SVGSVGElement>;

export function WerewolfEmblem(props: IconProps) {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.75" strokeLinecap="round" strokeLinejoin="round" {...props}>
      <path d="M4 3.5L7.2 10.5L5 15.8L12 20.5L19 15.8L16.8 10.5L20 3.5L14.8 6.8L12 5L9.2 6.8L4 3.5Z" />
      <path d="M8.8 12.2L10.5 13.2" />
      <path d="M15.2 12.2L13.5 13.2" />
      <path d="M10.5 16.5L12 17.8L13.5 16.5" />
    </svg>
  );
}

export function SeerEmblem(props: IconProps) {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.75" strokeLinecap="round" strokeLinejoin="round" {...props}>
      <path d="M2.5 12C4.8 7.4 8.2 5.5 12 5.5C15.8 5.5 19.2 7.4 21.5 12C19.2 16.6 15.8 18.5 12 18.5C8.2 18.5 4.8 16.6 2.5 12Z" />
      <circle cx="12" cy="12" r="3.2" />
      <path d="M12 2.2V3.8" />
      <path d="M12 20.2V21.8" />
      <path d="M5.5 4.8L6.6 6" />
      <path d="M18.5 4.8L17.4 6" />
    </svg>
  );
}

export function WitchEmblem(props: IconProps) {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.75" strokeLinecap="round" strokeLinejoin="round" {...props}>
      <path d="M9.5 3H14.5" />
      <path d="M10.5 3V7.8L5.6 15.2C4.7 16.6 5.7 18.5 7.4 18.5H16.6C18.3 18.5 19.3 16.6 18.4 15.2L13.5 7.8V3" />
      <path d="M7.2 14.2C9.5 13.2 14.5 15.2 16.8 14.2" />
      <circle cx="10.5" cy="16.3" r="0.8" fill="currentColor" />
      <circle cx="13.8" cy="11.8" r="0.7" fill="currentColor" />
    </svg>
  );
}

export function HunterEmblem(props: IconProps) {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.75" strokeLinecap="round" strokeLinejoin="round" {...props}>
      <circle cx="12" cy="12" r="7.5" />
      <circle cx="12" cy="12" r="2.2" />
      <path d="M12 2V5.5" />
      <path d="M12 18.5V22" />
      <path d="M2 12H5.5" />
      <path d="M18.5 12H22" />
    </svg>
  );
}

export function VillagerEmblem(props: IconProps) {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.75" strokeLinecap="round" strokeLinejoin="round" {...props}>
      <path d="M12 3L5.5 6V11.8C5.5 16.2 8.3 19.6 12 21C15.7 19.6 18.5 16.2 18.5 11.8V6L12 3Z" />
      <path d="M12 7.5V16.5" />
      <path d="M9.2 10.2L12 12.5L14.8 10.2" />
      <path d="M9.5 13.5L12 15.6L14.5 13.5" />
    </svg>
  );
}

export function TarotBackEmblem(props: IconProps) {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.65" strokeLinecap="round" strokeLinejoin="round" {...props}>
      <rect x="4.5" y="2.5" width="15" height="19" rx="2.5" />
      <path d="M12 6.5L13.6 10.4L17.5 12L13.6 13.6L12 17.5L10.4 13.6L6.5 12L10.4 10.4L12 6.5Z" />
    </svg>
  );
}

export function RoleEmblem({ role, className }: { role?: Role; className?: string }) {
  switch (role) {
    case "WEREWOLF":
      return <WerewolfEmblem className={className} />;
    case "SEER":
      return <SeerEmblem className={className} />;
    case "WITCH":
      return <WitchEmblem className={className} />;
    case "HUNTER":
      return <HunterEmblem className={className} />;
    case "VILLAGER":
      return <VillagerEmblem className={className} />;
    default:
      return <TarotBackEmblem className={className} />;
  }
}

export function MoonStarIcon(props: IconProps) {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.85" strokeLinecap="round" strokeLinejoin="round" {...props}>
      <path d="M19.5 14.5A7.5 7.5 0 0 1 9.5 4.5a7.5 7.5 0 1 0 10 10Z" />
      <path d="M17 4v3" />
      <path d="M15.5 5.5h3" />
    </svg>
  );
}

export function SunCrestIcon(props: IconProps) {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.85" strokeLinecap="round" strokeLinejoin="round" {...props}>
      <circle cx="12" cy="12" r="4.2" />
      <path d="M12 2.5v2.3M12 19.2v2.3M4.9 4.9l1.6 1.6M17.5 17.5l1.6 1.6M2.5 12h2.3M19.2 12h2.3M4.9 19.1l1.6-1.6M17.5 6.5l1.6-1.6" />
    </svg>
  );
}

export function CrownTrophyIcon(props: IconProps) {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.85" strokeLinecap="round" strokeLinejoin="round" {...props}>
      <path d="M3.5 18.5h17" />
      <path d="M4.5 15.5L3 6.5l5 3.5L12 4.5l4 5.5 5-3.5-1.5 9H4.5Z" />
    </svg>
  );
}

export function EyeToggleIcon({ hidden, className }: { hidden: boolean; className?: string }) {
  if (hidden) {
    return (
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" className={className}>
        <path d="M3 3l18 18" />
        <path d="M10.6 10.6a2 2 0 0 0 2.8 2.8" />
        <path d="M9.9 5.1A10.4 10.4 0 0 1 12 4.9c5 0 8.8 3.6 10 7.1a11.7 11.7 0 0 1-4 5.2" />
        <path d="M6.2 6.2A11.5 11.5 0 0 0 2 12c1.2 3.5 5 7.1 10 7.1 1.8 0 3.5-.5 5-1.3" />
      </svg>
    );
  }
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" className={className}>
      <path d="M2 12s3.8-7 10-7 10 7 10 7-3.8 7-10 7-10-7-10-7Z" />
      <circle cx="12" cy="12" r="2.8" />
    </svg>
  );
}

export function ChevronIcon({ open, className }: { open: boolean; className?: string }) {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      className={className}
      style={{ transform: open ? "rotate(180deg)" : "rotate(0deg)", transition: "transform 0.2s ease" }}
    >
      <path d="m18 15-6-6-6 6" />
    </svg>
  );
}
