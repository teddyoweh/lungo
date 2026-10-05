// The part of noVNC's viewer the app uses (the package ships no types).
declare module "@novnc/novnc" {
  export default class RFB extends EventTarget {
    constructor(target: HTMLElement, urlOrChannel: string | WebSocket, options?: { shared?: boolean; credentials?: { username?: string; password?: string; target?: string }; wsProtocols?: string[] });
    viewOnly: boolean;
    focusOnClick: boolean;
    clipViewport: boolean;
    dragViewport: boolean;
    scaleViewport: boolean;
    resizeSession: boolean;
    showDotCursor: boolean;
    background: string;
    qualityLevel: number;
    compressionLevel: number;
    disconnect(): void;
    sendCredentials(credentials: { username?: string; password?: string }): void;
    focus(): void;
    blur(): void;
    clipboardPasteFrom(text: string): void;
  }
}
