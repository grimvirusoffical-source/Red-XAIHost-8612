import { Colors, type ThemeColors } from "@/constants/theme";
import { useColorScheme } from "@/hooks/use-color-scheme";

/**
 * Returns the color palette for the active system color scheme (light/dark).
 *
 * ```tsx
 * const colors = useColors();
 * <View style={{ backgroundColor: colors.background }}>
 *   <Text style={{ color: colors.foreground }}>Hello</Text>
 * </View>
 * ```
 */
export function useColors(): ThemeColors {
  const scheme = useColorScheme() ?? "light";
  return Colors[scheme];
}
