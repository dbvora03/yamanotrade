import type { Metadata } from "next";
import "./globals.css";

export const metadata: Metadata = {
  title: "Yamanote — Loop Explorer",
  description: "A visual, section-level Yamanote line explorer."
};

export default function RootLayout({ children }: Readonly<{ children: React.ReactNode }>) {
  return (
    <html lang="en">
      <body>{children}</body>
    </html>
  );
}
