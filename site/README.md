# PIK.TAK site

这是项目介绍页，使用原生 HTML、CSS 和 JavaScript，不需要安装依赖或构建。

## Cloudflare Pages

在 Pages 项目中选择 **Connect to Git**：

- Root directory：`site`
- Build command：留空
- Build output directory：`.`

或者使用 Wrangler：

```sh
npx wrangler pages deploy site --project-name piktak
```
