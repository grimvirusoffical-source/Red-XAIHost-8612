import { JSDOM } from "jsdom";
import type { Plugin } from "vite";

export default function runableAnalyticsPlugin(): Plugin {
  return {
    name: "runable-analytics-plugin",
    enforce: "pre",
    async transformIndexHtml(html) {
      const dom = new JSDOM(html);
      const doc = dom.window.document;
      const head = doc.head;

      const applicationId = process.env.APPLICATION_ID ?? "";
      const hostname = applicationId ? `${applicationId}-website` : "localhost";

      // Optional analytics is loaded only after the signed-in user explicitly
      // opts in through the Red-XAI consent prompt.
      const script = doc.createElement("script");
      script.defer = true;
      script.src = "/analytics-consent.js";
      script.dataset.hostname = hostname;
      script.dataset.url = "https://r.lilstts.com/events";
      if (hostname === "localhost") {
        script.dataset.debug = hostname;
      }
      head.appendChild(script);

      return dom.serialize();
    },
  };
}
