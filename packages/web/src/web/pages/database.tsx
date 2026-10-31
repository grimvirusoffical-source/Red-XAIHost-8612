import { Database, ExternalLink, KeyRound } from "lucide-react";
import { PageHeader } from "../components/shell";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";

export default function DatabasePage() {
  return (
    <>
      <PageHeader
        eyebrow="Red-XAI"
        title="Database"
        subtitle="Your RedX database entry point stays inside the Red-XAI workspace. Website access follows your signed-in account; API keys are only for CLI, SDK and third-party integrations."
      />
      <div className="grid gap-4 lg:grid-cols-2">
        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2">
              <Database className="size-4 text-primary" />
              Account database workspace
            </CardTitle>
          </CardHeader>
          <CardBody>
            <p className="mb-4 text-sm leading-relaxed text-muted-foreground">
              Red-XAI uses your verified account identity to route you to the RedX console for this account. You are not asked to paste a database API key just to use the website.
            </p>
            <Button asChild>
              <a href="/api/db/open" target="_blank" rel="noreferrer">
                <ExternalLink className="size-4" />
                Open full database workspace
              </a>
            </Button>
            <p className="mt-3 text-xs text-muted-foreground">
              The full RedX console opens separately because its anti-framing policy intentionally prevents embedding administrative pages.
            </p>
          </CardBody>
        </Card>
        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2">
              <KeyRound className="size-4 text-primary" />
              Developer access
            </CardTitle>
          </CardHeader>
          <CardBody>
            <p className="text-sm leading-relaxed text-muted-foreground">
              Generate scoped API credentials only when an external app, SDK or CLI needs database access. Website sign-in and database ownership remain account-based and do not depend on those keys.
            </p>
          </CardBody>
        </Card>
      </div>
    </>
  );
}
