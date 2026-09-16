import type { Metadata } from "next";
import { Space_Grotesk } from "next/font/google";
import "./globals.css";

const spaceGrotesk = Space_Grotesk({
  subsets: ["latin"],
  weight: ["500"],
  display: "swap",
  variable: "--font-space-grotesk"
});

export const metadata: Metadata = {
  title: "Yamanote — Loop Explorer",
  description: "A visual, section-level Yamanote line explorer."
};

export default function RootLayout({ children }: Readonly<{ children: React.ReactNode }>) {
  return (
    <html lang="en">
      <body className={spaceGrotesk.variable}>{children}</body>
    </html>
  );
}
