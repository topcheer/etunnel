// Build-time stub for the two HarmonyOS SDK modules the SDK imports
// (@ohos.net.websocket / @ohos.net.http). Mirrors the SDK d.ts shape
// (`declare namespace` + default export, which ArkTS also imports by name).
// Build-only: the HAR ships compiled JS (index.js / src/main/ets/*.js) and
// never this file. Consumers compile against the real SDK typings.
declare module '@ohos.net.websocket' {
  export namespace webSocket {
    interface WebSocketRequestOptions {
      header?: Object;
      method?: string;
      protocols?: Array<string>;
    }
    interface WebSocket {
      url: string;
      connect(url: string, options?: WebSocketRequestOptions,
        callback?: (err: Error | null, result: boolean) => void): void;
      on(type: string, callback: (...args: any[]) => void): void;
      off(type: string, callback?: (...args: any[]) => void): void;
      send(data: string | ArrayBuffer, callback?: (err: Error | null, result: boolean) => void): void;
      close(code?: number, reason?: string, callback?: (err: Error | null, result: boolean) => void): void;
    }
    function createWebSocket(): WebSocket;
  }
  export default webSocket;
}

declare module '@ohos.net.http' {
  export namespace http {
    enum HttpDataType { STRING, OBJECT, ARRAY_BUFFER }
    enum HttpMethod { GET, POST, PUT, DELETE, HEAD, OPTIONS, CONNECT, TRACE }
    enum ResponseCode { OK = 200 }
    interface HttpRequestOptions {
      method?: HttpMethod | string;
      header?: Object;
      extraData?: string | Object | ArrayBuffer;
      expectDataType?: HttpDataType;
      usingCache?: boolean;
      connectTimeout?: number;
      readTimeout?: number;
      [key: string]: Object | undefined;
    }
    interface HttpResponse {
      responseCode: number;
      header: Object;
      result: string | Object | ArrayBuffer;
      cookies: string;
    }
    interface HttpRequest {
      request(url: string, options?: HttpRequestOptions): Promise<HttpResponse>;
      destroy(): void;
      on(type: string, callback: (...args: any[]) => void): void;
      off(type: string, callback?: (...args: any[]) => void): void;
    }
    function createHttp(): HttpRequest;
  }
  export default http;
}
