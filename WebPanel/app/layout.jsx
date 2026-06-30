import "./globals.css";

export const metadata = {
  title: "SanaeNyaProbe 监控",
  description: "SanaeNyaProbe server monitor",
};

export default function RootLayout({ children }) {
  return (
    <html lang="zh-CN">
      <body>{children}</body>
    </html>
  );
}
