import React, { Component, type ErrorInfo, type ReactNode } from "react";
import {
  View,
  Text,
  ScrollView,
  TouchableOpacity,
  Platform,
  Dimensions,
  StyleSheet,
} from "react-native";

interface Props {
  children: ReactNode;
}

interface ErrorEntry {
  message: string;
  stack?: string;
  componentStack?: string;
}

interface State {
  errors: ErrorEntry[];
  expanded: boolean;
  expandedIndex: number | null;
  copied: boolean;
}

export class ErrorBoundary extends Component<Props, State> {
  state: State = { errors: [], expanded: false, expandedIndex: null, copied: false };

  private onError = (e: ErrorEvent) => {
    this.addError({
      message: e.message || String(e.error),
      stack: e.error?.stack,
    });
  };

  private onUnhandledRejection = (e: PromiseRejectionEvent) => {
    const err = e.reason;
    this.addError({
      message: err?.message || String(err),
      stack: err?.stack,
    });
  };

  private addError(entry: ErrorEntry) {
    this.setState((prev) => {
      // Dedupe by message
      if (prev.errors.some((e) => e.message === entry.message)) return null;
      return { errors: [...prev.errors, entry] };
    });
  }

  componentDidMount() {
    if (Platform.OS === "web" && typeof window !== "undefined") {
      window.addEventListener("error", this.onError);
      window.addEventListener("unhandledrejection", this.onUnhandledRejection);
    }
  }

  componentWillUnmount() {
    if (Platform.OS === "web" && typeof window !== "undefined") {
      window.removeEventListener("error", this.onError);
      window.removeEventListener("unhandledrejection", this.onUnhandledRejection);
    }
  }

  static getDerivedStateFromError(error: Error) {
    return {
      errors: [{ message: error.message, stack: error.stack }],
      expanded: false,
      expandedIndex: null,
    };
  }

  componentDidCatch(error: Error, errorInfo: ErrorInfo) {
    this.setState((prev) => {
      const updated = prev.errors.map((e) =>
        e.message === error.message
          ? { ...e, componentStack: errorInfo.componentStack ?? undefined }
          : e,
      );
      return { errors: updated };
    });
  }

  private getAllErrorText() {
    return this.state.errors
      .map((e, i) => {
        let text = `Error ${i + 1}: ${e.message}`;
        if (e.stack) text += `\n${e.stack}`;
        if (e.componentStack) text += `\nComponent Stack:${e.componentStack}`;
        return text;
      })
      .join("\n\n────────────────\n\n");
  }

  private copy = async () => {
    try {
      await navigator.clipboard.writeText(this.getAllErrorText());
      this.setState({ copied: true });
      setTimeout(() => this.setState({ copied: false }), 2000);
    } catch {}
  };

  private retry = () => {
    this.setState({ errors: [], expanded: false, expandedIndex: null, copied: false });
  };

  render() {
    const { errors, expanded, expandedIndex, copied } = this.state;

    if (errors.length === 0 || Platform.OS !== "web") return this.props.children;

    const { height } = Dimensions.get("window");
    const detailsMaxHeight = Math.min(height * 0.5, 440);

    return (
      <View style={s.screen}>
        {/* Crash background */}
        <View style={s.pageBg}>
          <View style={s.pageContent}>
            <Text style={s.sadFace}>:(</Text>
            <Text style={s.crashTitle}>Something went wrong</Text>
            <Text style={s.crashSub} numberOfLines={3}>
              {errors[0].message}
            </Text>
          </View>
        </View>

        {/* Bottom toast area */}
        <View style={s.toastWrapper}>
          {/* Expanded error list */}
          {expanded && (
            <View style={[s.detailsPanel, { maxHeight: detailsMaxHeight }]}>
              <View style={s.detailsHeader}>
                <Text style={s.detailsTitle}>
                  {errors.length} Error{errors.length > 1 ? "s" : ""}
                </Text>
                <TouchableOpacity style={s.retryBtn} onPress={this.retry}>
                  <Text style={s.retryText}>Retry</Text>
                </TouchableOpacity>
              </View>

              <ScrollView nestedScrollEnabled showsVerticalScrollIndicator>
                {errors.map((err, i) => {
                  const isOpen = expandedIndex === i;
                  return (
                    <View key={i} style={s.errorCard}>
                      <TouchableOpacity
                        style={s.errorCardHeader}
                        onPress={() => this.setState({ expandedIndex: isOpen ? null : i })}
                        activeOpacity={0.7}
                      >
                        <Text style={s.chevron}>{isOpen ? "▾" : "▸"}</Text>
                        <Text style={s.errorMsg} numberOfLines={isOpen ? undefined : 2}>
                          {err.message}
                        </Text>
                      </TouchableOpacity>

                      {isOpen && (
                        <ScrollView style={s.stackScroll} nestedScrollEnabled horizontal={false}>
                          <Text style={s.stackText} selectable>
                            {err.stack}
                            {err.componentStack && `\n\n── Component Stack ──${err.componentStack}`}
                          </Text>
                        </ScrollView>
                      )}
                    </View>
                  );
                })}
              </ScrollView>
            </View>
          )}

          {/* Toast bar */}
          <TouchableOpacity
            style={s.toast}
            onPress={() => this.setState((p) => ({ expanded: !p.expanded }))}
            activeOpacity={0.85}
          >
            <View style={s.toastDot} />
            <View style={s.toastBody}>
              <Text style={s.toastTitle} numberOfLines={1}>
                Found {errors.length} critical error{errors.length > 1 ? "s" : ""}
              </Text>
              <Text style={s.toastSub} numberOfLines={1}>
                {expanded ? "Tap to collapse" : "Tap to see details"}
              </Text>
            </View>
            <TouchableOpacity style={s.copyBtn} onPress={this.copy} activeOpacity={0.7}>
              <Text style={s.copyText}>{copied ? "Copied!" : "Copy"}</Text>
            </TouchableOpacity>
          </TouchableOpacity>
        </View>
      </View>
    );
  }
}

const s = StyleSheet.create({
  screen: {
    flex: 1,
    backgroundColor: "#0a0a0a",
  },
  pageBg: {
    flex: 1,
    justifyContent: "center",
    alignItems: "center",
    opacity: 0.6,
    paddingHorizontal: "8%",
  },
  pageContent: {
    alignItems: "center",
    width: "100%",
    maxWidth: 480,
  },
  sadFace: {
    fontSize: 48,
    color: "#666",
    marginBottom: 16,
  },
  crashTitle: {
    fontSize: 22,
    fontWeight: "700",
    color: "#fff",
    marginBottom: 8,
  },
  crashSub: {
    fontSize: 14,
    color: "#888",
    textAlign: "center",
    lineHeight: 20,
    paddingHorizontal: 16,
  },
  toastWrapper: {
    position: "absolute",
    bottom: 0,
    left: 0,
    right: 0,
  },

  // Expanded panel
  detailsPanel: {
    backgroundColor: "#1c1c1e",
    borderTopLeftRadius: 16,
    borderTopRightRadius: 16,
    paddingTop: 16,
    paddingHorizontal: 16,
  },
  detailsHeader: {
    flexDirection: "row",
    justifyContent: "space-between",
    alignItems: "center",
    marginBottom: 12,
  },
  detailsTitle: {
    fontSize: 13,
    fontWeight: "600",
    color: "#aaa",
    textTransform: "uppercase",
    letterSpacing: 0.5,
  },
  retryBtn: {
    backgroundColor: "#2c2c2e",
    borderRadius: 8,
    paddingVertical: 6,
    paddingHorizontal: 14,
  },
  retryText: {
    color: "#fff",
    fontSize: 13,
    fontWeight: "600",
  },

  // Collapsible error cards
  errorCard: {
    backgroundColor: "#111113",
    borderRadius: 10,
    marginBottom: 8,
    overflow: "hidden",
  },
  errorCardHeader: {
    flexDirection: "row",
    alignItems: "flex-start",
    padding: 12,
  },
  chevron: {
    fontSize: 14,
    color: "#666",
    marginRight: 8,
    marginTop: 1,
    width: 14,
  },
  errorMsg: {
    fontSize: 13,
    color: "#ff6b6b",
    flex: 1,
    lineHeight: 19,
  },
  stackScroll: {
    backgroundColor: "#0d0d0f",
    paddingHorizontal: 14,
    paddingVertical: 10,
    maxHeight: 180,
    borderTopWidth: StyleSheet.hairlineWidth,
    borderTopColor: "#2a2a2a",
  },
  stackText: {
    fontSize: 11,
    color: "#d4d4d8",
    fontFamily: "monospace",
    lineHeight: 17,
  },

  // Toast bar
  toast: {
    flexDirection: "row",
    alignItems: "center",
    backgroundColor: "#1c1c1e",
    borderTopWidth: StyleSheet.hairlineWidth,
    borderTopColor: "rgba(255,59,48,0.5)",
    paddingVertical: 14,
    paddingHorizontal: 16,
    paddingBottom: 28,
  },
  toastDot: {
    width: 10,
    height: 10,
    borderRadius: 5,
    backgroundColor: "#ff3b30",
    marginRight: 12,
    flexShrink: 0,
  },
  toastBody: {
    flex: 1,
    marginRight: 12,
  },
  toastTitle: {
    fontSize: 14,
    fontWeight: "600",
    color: "#ff453a",
  },
  toastSub: {
    fontSize: 12,
    color: "#888",
    marginTop: 2,
  },
  copyBtn: {
    backgroundColor: "#2c2c2e",
    borderRadius: 8,
    paddingVertical: 8,
    paddingHorizontal: 16,
    flexShrink: 0,
  },
  copyText: {
    color: "#fff",
    fontSize: 13,
    fontWeight: "600",
  },
});
