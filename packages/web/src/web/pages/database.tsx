import { useEffect } from "react";
import { Database, ExternalLink } from "lucide-react";
import { PageHeader } from "../components/shell";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";

export default function DatabasePage() {
  useEffect(() => {
    const timer = window.setTimeout(() => {
      window.location.assign("/api/db/open");
    }, 350);
    return () => window.clearTimeout(timer);
  }, []);

  return (
    <>
      <PageHeader
        eyebrow="Red-XAI"
        title="Database"
        subtitle="Opening your account-scoped RedX workspace. No database API key is required for website access."
      />
      <Card>
        <CardHeader><CardTitle className="flex items-center gap-2"><Database className="size-4 text-primary" /> Your database workspace</CardTitle></CardHeader>
        <CardBody>
          <p className="mb-4 text-sm text-muted-foreground">
            RedX uses your Red-XAI account identity to show only databases owned by this account. Developer API keys remain available for CLI, SDK and third-party integrations.
          </p>
          <Button asChild><a href="/api/db/open"><ExternalLink className="size-4" /> Open database workspace</a></Button>
        </CardBody>
      </Card>
    </>
  );
}
