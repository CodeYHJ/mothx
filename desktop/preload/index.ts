import { contextBridge, ipcRenderer } from 'electron';

contextBridge.exposeInMainWorld('__MOTHX_DESKTOP__', {
  isDesktop: true,
  version: process.env.npm_package_version || 'dev',
  chooseDirectory: (defaultPath = '') => ipcRenderer.invoke('desktop:choose-directory', defaultPath),
  onUpdateAvailable: (callback: (info: unknown) => void) => {
    const listener = (_event: Electron.IpcRendererEvent, info: unknown) => callback(info);
    ipcRenderer.on('desktop:update-available', listener);
    return () => ipcRenderer.removeListener('desktop:update-available', listener);
  },
});
