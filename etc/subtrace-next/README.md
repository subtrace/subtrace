This package allows Next.js projects deployed on Vercel to use [Subtrace](https://github.com/subtrace/subtrace).

Here's how you can get started:

1. Install this package using `npm install subtrace-next` or `yarn add subtrace-next`.
2. Add `import "subtrace-next"` as a top level import in your app.
3. To trace incoming requests, you can wrap your requests handlers with the `trace` function:

```TypeScript
import { NextRequest, NextResponse } from "next/server";

import { trace } from "subtrace-next";

export const GET = trace((request: NextRequest) => {
  // Get query parameters
  const { searchParams } = new URL(request.url);
  const name = searchParams.get("name") || "World";

  const responseData = {
    message: `Hello ${name}!`,
    method: "GET",
    timestamp: new Date().toISOString(),
    query: Object.fromEntries(searchParams.entries()),
  };

  return NextResponse.json(responseData);
});
```

4. On Vercel, add the `SUBTRACE_TOKEN` environment variable. You can get a token on the **Tokens** page of the
   [dashboard](https://subtrace.dev/dashboard).

5. Deploy your ap as you normally do.

That's it! You should see your app's requests show up in real time on the Subtrace dashboard.
