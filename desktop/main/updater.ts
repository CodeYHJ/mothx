import { app, dialog } from 'electron';
import { autoUpdater } from 'electron-updater';

export function startUpdater(): void {
  if (!app.isPackaged) return;
  autoUpdater.autoDownload = false;
  autoUpdater.on('update-available', async () => {
    const result = await dialog.showMessageBox({
      type: 'info',
      buttons: ['Download', 'Later'],
      defaultId: 0,
      cancelId: 1,
      title: 'MothX update available',
      message: 'A new MothX desktop version is available.',
    });
    if (result.response === 0) await autoUpdater.downloadUpdate();
  });
  autoUpdater.on('update-downloaded', async () => {
    const result = await dialog.showMessageBox({
      type: 'info',
      buttons: ['Restart', 'Later'],
      defaultId: 0,
      cancelId: 1,
      title: 'MothX update ready',
      message: 'The update is ready to install.',
    });
    if (result.response === 0) autoUpdater.quitAndInstall();
  });
  autoUpdater.on('error', (error: Error) => console.error('[desktop updater]', error));
  void autoUpdater.checkForUpdates();
}
