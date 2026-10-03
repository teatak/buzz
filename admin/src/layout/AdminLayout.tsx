import { useState, type ReactNode } from "react";
import { LogOut, KeyRound, UserRound } from "lucide-react";
import { AppSidebar } from "../components/app-sidebar";
import { Avatar, AvatarFallback } from "../components/ui/avatar";
import {
  Breadcrumb,
  BreadcrumbItem,
  BreadcrumbLink,
  BreadcrumbList,
  BreadcrumbPage,
  BreadcrumbSeparator,
} from "../components/ui/breadcrumb";
import { Button } from "../components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "../components/ui/dropdown-menu";
import { Separator } from "../components/ui/separator";
import { SidebarInset, SidebarProvider, SidebarTrigger } from "../components/ui/sidebar";
import { TooltipProvider } from "../components/ui/tooltip";
import { useLocale } from "../i18n/locale";
import { LocaleToggle } from "../i18n/LocaleToggle";
import { ThemeToggle } from "../theme/ThemeToggle";
import type { Session, View } from "../types/admin";

function readSidebarDefaultOpen() {
  if (typeof document === "undefined") {
    return false;
  }
  const state = document.cookie
    .split("; ")
    .find((part) => part.startsWith("sidebar_state="))
    ?.split("=")[1];
  return state === "true";
}

export function AdminLayout(props: {
  children: ReactNode;
  session: Session;
  title: string;
  view: View;
  version?: string;
  onNavigate: (view: View) => void;
  onChangePassword: () => void;
  onLogout: () => void;
}) {
  const { t } = useLocale();
  const [sidebarDefaultOpen] = useState(readSidebarDefaultOpen);

  return (
    <TooltipProvider>
      <SidebarProvider
        defaultOpen={sidebarDefaultOpen}
        className="[&_[data-slot=sidebar-container]]:duration-300 [&_[data-slot=sidebar-container]]:ease-in-out [&_[data-slot=sidebar-gap]]:duration-300 [&_[data-slot=sidebar-gap]]:ease-in-out [&_[data-slot=sidebar-group-label]]:duration-300 [&_[data-slot=sidebar-group-label]]:ease-in-out [&_[data-slot=sidebar-menu-button]]:duration-300 [&_[data-slot=sidebar-menu-button]]:ease-in-out"
      >
        <AppSidebar role={props.session.user.role} view={props.view} version={props.version} onNavigate={props.onNavigate} />
        <SidebarInset className="min-w-0">
          <header className="sticky top-0 z-30 flex h-16 shrink-0 items-center gap-2 border-b bg-background px-4 transition-[height,padding] duration-300 ease-in-out">
            <SidebarTrigger className="-ml-1" />
            <Separator orientation="vertical" className="mr-2 data-vertical:h-4 data-vertical:self-center" />
            <Breadcrumb>
              <BreadcrumbList>
                <BreadcrumbItem className="hidden md:block">
                  <BreadcrumbLink href="#">Buzz Admin</BreadcrumbLink>
                </BreadcrumbItem>
                <BreadcrumbSeparator className="hidden md:block" />
                <BreadcrumbItem>
                  <BreadcrumbPage>{props.title}</BreadcrumbPage>
                </BreadcrumbItem>
              </BreadcrumbList>
            </Breadcrumb>
            <div className="toolbar ml-auto">
              <ThemeToggle />
              <LocaleToggle />
              <DropdownMenu>
                <DropdownMenuTrigger asChild>
                  <Button variant="ghost" size="icon" type="button" aria-label={t("common.user_menu")} className="rounded-full">
                    <Avatar>
                      <AvatarFallback className="bg-indigo-100 text-indigo-700 dark:bg-indigo-950 dark:text-indigo-300">
                        <UserRound size={16} />
                      </AvatarFallback>
                    </Avatar>
                  </Button>
                </DropdownMenuTrigger>
                <DropdownMenuContent align="end" className="grid w-56 gap-1">
                  <DropdownMenuLabel>{props.session.user.username}</DropdownMenuLabel>
                  <DropdownMenuSeparator />
                  <DropdownMenuItem onSelect={props.onChangePassword}>
                    <KeyRound /> {t("user.change_password")}
                  </DropdownMenuItem>
                  <DropdownMenuItem onSelect={props.onLogout}>
                    <LogOut /> {t("user.logout")}
                  </DropdownMenuItem>
                </DropdownMenuContent>
              </DropdownMenu>
            </div>
          </header>
          {props.children}
        </SidebarInset>
      </SidebarProvider>
    </TooltipProvider>
  );
}
