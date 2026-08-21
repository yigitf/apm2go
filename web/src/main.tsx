import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createBrowserRouter, RouterProvider } from "react-router-dom";

import "./theme.css";
import { Layout } from "./components/Layout";
import { SidebarProvider } from "./components/SidebarProvider";
import { ThemeProvider } from "./components/ThemeProvider";
import { TimeRangeProvider } from "./components/TimeRange";
import { Overview } from "./pages/Overview";
import { Services } from "./pages/Services";
import { ServiceDetail } from "./pages/ServiceDetail";
import { Traces } from "./pages/Traces";
import { TraceDetail } from "./pages/TraceDetail";
import { ServiceMap } from "./pages/ServiceMap";
import { Host } from "./pages/Host";
import { Jvms } from "./pages/Jvms";
import { JvmDetail } from "./pages/JvmDetail";
import { Settings } from "./pages/Settings";

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      // Monitoring data is always slightly stale; refetching on every window
      // focus would hammer the API without telling the operator anything new.
      refetchOnWindowFocus: false,
      staleTime: 5_000,
      retry: 1,
    },
  },
});

const router = createBrowserRouter([
  {
    path: "/",
    element: <Layout />,
    children: [
      { index: true, element: <Overview /> },
      { path: "services", element: <Services /> },
      { path: "services/:service", element: <ServiceDetail /> },
      { path: "traces", element: <Traces /> },
      { path: "traces/:traceId", element: <TraceDetail /> },
      { path: "map", element: <ServiceMap /> },
      { path: "host", element: <Host /> },
      { path: "jvms", element: <Jvms /> },
      { path: "jvms/:pid", element: <JvmDetail /> },
      { path: "settings", element: <Settings /> },
    ],
  },
]);

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <ThemeProvider>
        <SidebarProvider>
          <TimeRangeProvider>
            <RouterProvider router={router} />
          </TimeRangeProvider>
        </SidebarProvider>
      </ThemeProvider>
    </QueryClientProvider>
  </StrictMode>,
);
