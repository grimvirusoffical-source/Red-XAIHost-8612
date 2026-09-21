import { Platform } from "react-native";

/**
 * App color tokens. Recolor these to brand the app — every screen and component
 * reads from here via `useColors()` (see `hooks/use-colors.ts`), so changing a
 * value here updates the whole app in both light and dark mode.
 *
 * The token names mirror the web app's design tokens (`packages/web/src/web/styles.css`)
 * so the two platforms share one vocabulary: `background`, `foreground`, `card`,
 * `primary`, `muted`, `border`, etc. Values are plain hex/rgba strings — React
 * Native's StyleSheet does not support CSS color functions like `oklch()`.
 */
export const Colors = {
  light: {
    background: "#FFFFFF",
    foreground: "#171717",
    card: "#FFFFFF",
    cardForeground: "#171717",
    primary: "#1F1F1F",
    primaryForeground: "#FAFAFA",
    secondary: "#F5F5F5",
    secondaryForeground: "#1F1F1F",
    muted: "#F5F5F5",
    mutedForeground: "#737373",
    accent: "#F5F5F5",
    accentForeground: "#1F1F1F",
    border: "#E5E5E5",
    destructive: "#DC2626",
    success: "#16A34A",
    warning: "#D97706",
  },
  dark: {
    background: "#0A0A0A",
    foreground: "#FAFAFA",
    card: "#1A1A1A",
    cardForeground: "#FAFAFA",
    primary: "#E5E5E5",
    primaryForeground: "#1F1F1F",
    secondary: "#262626",
    secondaryForeground: "#FAFAFA",
    muted: "#262626",
    mutedForeground: "#A3A3A3",
    accent: "#262626",
    accentForeground: "#FAFAFA",
    border: "#262626",
    destructive: "#EF4444",
    success: "#22C55E",
    warning: "#F59E0B",
  },
} as const;

export type ColorScheme = keyof typeof Colors;
export type ThemeColors = (typeof Colors)[ColorScheme];

/**
 * Platform-appropriate font families. Use for `fontFamily` in styles, or load a
 * custom font with `useFonts` from `expo-font` and reference it here.
 */
export const Fonts = Platform.select({
  ios: {
    sans: "system-ui",
    serif: "ui-serif",
    rounded: "ui-rounded",
    mono: "ui-monospace",
  },
  default: {
    sans: "normal",
    serif: "serif",
    rounded: "normal",
    mono: "monospace",
  },
  web: {
    sans: "system-ui, -apple-system, 'Segoe UI', Roboto, sans-serif",
    serif: "Georgia, 'Times New Roman', serif",
    rounded: "'SF Pro Rounded', 'Hiragino Maru Gothic ProN', sans-serif",
    mono: "'SF Mono', 'Roboto Mono', monospace",
  },
});
