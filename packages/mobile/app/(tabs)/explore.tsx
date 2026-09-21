import { StyleSheet, Text, View } from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import { useColors } from "@/hooks/use-colors";

export default function Explore() {
  const colors = useColors();

  return (
    <SafeAreaView
      edges={["top", "left", "right"]}
      style={[styles.container, { backgroundColor: colors.background }]}
    >
      <Text style={[styles.title, { color: colors.foreground }]}>Explore</Text>
      <Text style={[styles.body, { color: colors.mutedForeground }]}>
        A second screen showing the SafeAreaView + useColors() pattern. Add your own screens as
        files under app/(tabs)/ or app/.
      </Text>

      <View style={[styles.pill, { backgroundColor: colors.secondary }]}>
        <Text style={{ color: colors.secondaryForeground }}>Themed surface</Text>
      </View>
    </SafeAreaView>
  );
}

const styles = StyleSheet.create({
  container: {
    flex: 1,
    padding: 24,
    gap: 12,
  },
  title: {
    fontSize: 28,
    fontWeight: "700",
  },
  body: {
    fontSize: 15,
    lineHeight: 22,
  },
  pill: {
    alignSelf: "flex-start",
    paddingVertical: 8,
    paddingHorizontal: 16,
    borderRadius: 999,
    marginTop: 8,
  },
});
