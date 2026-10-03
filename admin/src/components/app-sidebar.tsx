import { Boxes, Gauge, KeyRound, Server, UserRound } from "lucide-react";
import type { View } from "@/types/admin";
import { AppSidebarRail } from "@/components/app-sidebar-rail";
import { BrandIcon } from "@/components/brand-icons";
import { useLocale } from "@/i18n/locale";
import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarGroup,
  SidebarGroupContent,
  SidebarGroupLabel,
  SidebarHeader,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
} from "@/components/ui/sidebar";

type AppSidebarProps = {
  role: string;
  view: View;
  version?: string;
  onNavigate: (view: View) => void;
};

const items: Array<{ view: View; labelKey: string; icon: typeof Gauge; adminOnly?: boolean }> = [
  { view: "dashboard", labelKey: "nav.dashboard", icon: Gauge },
  { view: "myKeys", labelKey: "nav.my_keys", icon: KeyRound },
  { view: "users", labelKey: "nav.users", icon: UserRound, adminOnly: true },
  { view: "models", labelKey: "nav.models", icon: Boxes, adminOnly: true },
  { view: "providers", labelKey: "nav.providers", icon: Server, adminOnly: true },
];

export function AppSidebar({ role, view, version, onNavigate }: AppSidebarProps) {
  const { t } = useLocale();
  return (
    <Sidebar collapsible="icon">
      <SidebarHeader>
        <SidebarMenu>
          <SidebarMenuItem>
            <SidebarMenuButton size="lg" className="hover:bg-transparent hover:text-foreground">
              <BrandIcon name="buzz" className="size-8 transition-transform duration-300 ease-in-out" iconClassName="!size-full" />
              <div className="grid flex-1 text-left text-sm leading-tight transition-transform duration-300 ease-in-out">
                <span className="truncate font-semibold">Buzz</span>
                <span className="truncate text-xs text-muted-foreground">{t("app.subtitle")}</span>
              </div>
            </SidebarMenuButton>
          </SidebarMenuItem>
        </SidebarMenu>
      </SidebarHeader>
      <SidebarContent>
        <SidebarGroup>
          <SidebarGroupLabel>{t("common.admin")}</SidebarGroupLabel>
          <SidebarGroupContent>
            <SidebarMenu className="gap-1">
              {items.filter((item) => !item.adminOnly || role === "admin").map((item) => (
                <SidebarMenuItem key={item.view}>
                  <SidebarMenuButton
                    type="button"
                    isActive={view === item.view || (item.view === "users" && view === "userDetail")}
                    tooltip={t(item.labelKey)}
                    className="hover:bg-primary/10 hover:text-foreground dark:hover:bg-primary/25 dark:hover:text-white data-[active=true]:bg-indigo-600 data-[active=true]:text-white data-[active=true]:hover:bg-indigo-600 data-[active=true]:hover:text-white"
                    onClick={() => onNavigate(item.view)}
                  >
                    <item.icon />
                    <span>{t(item.labelKey)}</span>
                  </SidebarMenuButton>
                </SidebarMenuItem>
              ))}
            </SidebarMenu>
          </SidebarGroupContent>
        </SidebarGroup>
      </SidebarContent>
      {version && (
        <SidebarFooter className="group-data-[collapsible=icon]:hidden">
          <div className="px-2 py-1 text-xs text-muted-foreground/60 select-none">
            v{version}
          </div>
        </SidebarFooter>
      )}
      <AppSidebarRail />
    </Sidebar>
  );
}
