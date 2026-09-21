import { StyleSheet, Text, View, ActivityIndicator } from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import { usePing } from "@/queries/ping";
import { useColors } from "@/hooks/use-colors";

export default function Index() {
  const colors = useColors();
  const ping = usePing();

  return (
    <SafeAreaView
      edges={["top", "left", "right"]}
      style={[styles.container, { backgroundColor: colors.background }]}
    >
      <Text style={[styles.title, { color: colors.foreground }]}>Welcome</Text>

      <View style={[styles.card, { backgroundColor: colors.card, borderColor: colors.border }]}>
        {ping.isLoading ? (
          <ActivityIndicator color={colors.primary} />
        ) : ping.isError ? (
          <Text style={{ color: colors.destructive }}>API Error</Text>
        ) : (
          <Text style={{ color: colors.mutedForeground }}>API Status: {ping.data?.message}</Text>
        )}
      </View>
    </SafeAreaView>
  );
}

const styles = StyleSheet.create({
  container: {
    flex: 1,
    alignItems: "center",
    justifyContent: "center",
    padding: 24,
    gap: 16,
  },
  title: {
    fontSize: 28,
    fontWeight: "700",
  },
  card: {
    paddingVertical: 16,
    paddingHorizontal: 24,
    borderRadius: 12,
    borderWidth: 1,
  },
});
