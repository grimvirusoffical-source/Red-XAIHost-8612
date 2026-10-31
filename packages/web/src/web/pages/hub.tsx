import { Link } from "wouter";
import { Database, Server, Settings, Sparkles } from "lucide-react";
import { PageHeader } from "../components/shell";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";

const sections = [
  {
    href: "/host",
    title: "Host",
    description: "Projects, nodes, domains, deployments, builds and usage for this Red-XAI account.",
    icon: Server,
  },
  {
    href: "/db",
    title: "Database",
    description: "Your account-scoped RedX database entry point. Website access does not require a database API key.",
    icon: Database,
  },
  {
    href: "/vortex",
    title: "Vortex",
    description: "Current stable Vortex downloads for macOS, Windows and Linux.",
    icon: Sparkles,
  },
  {
    href: "/settings",
    title: "Account & settings",
    description: "Account identity, browser notifications, security status and your tenant-scoped Host integrations.",
    icon: Settings,
  },
];

export default function HubPage() {
  return (
    <>
      <PageHeader
        eyebrow="Red-XAI"
        title="Hub"
        subtitle="One signed-in workspace for Host, Database, Vortex and account settings. Your account stays isolated from every other Red-XAI tenant."
      />
      <div className="grid gap-4 md:grid-cols-2">
        {sections.map((section) => (
          <Card key={section.href} className="rise">
            <CardHeader>
              <CardTitle className="flex items-center gap-2">
                <section.icon className="size-4 text-primary" />
                {section.title}
              </CardTitle>
            </CardHeader>
            <CardBody className="flex min-h-32 flex-col">
              <p className="mb-5 flex-1 text-sm leading-relaxed text-muted-foreground">{section.description}</p>
              <Button asChild className="w-fit">
                <Link href={section.href}>Open {section.title}</Link>
              </Button>
            </CardBody>
          </Card>
        ))}
      </div>
    </>
  );
}
