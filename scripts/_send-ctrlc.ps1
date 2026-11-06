# Internal helper used by stop-kafka.ps1. Sends CTRL+C to the console of another process so a
# console JVM runs its shutdown hooks (a clean Kafka shutdown). It runs in its OWN process because
# attaching to another console detaches the caller from its own.
param([Parameter(Mandatory)][uint32]$ProcessId)

Add-Type -TypeDefinition @"
using System;
using System.Runtime.InteropServices;
public static class ConsoleCtrl {
    [DllImport("kernel32.dll", SetLastError = true)] static extern bool AttachConsole(uint pid);
    [DllImport("kernel32.dll", SetLastError = true)] static extern bool FreeConsole();
    [DllImport("kernel32.dll")] static extern bool SetConsoleCtrlHandler(IntPtr handler, bool add);
    [DllImport("kernel32.dll")] static extern bool GenerateConsoleCtrlEvent(uint ctrlEvent, uint processGroupId);
    public static int SendCtrlC(uint pid) {
        FreeConsole();
        if (!AttachConsole(pid)) return Marshal.GetLastWin32Error();
        SetConsoleCtrlHandler(IntPtr.Zero, true);          // this helper ignores the event itself
        bool ok = GenerateConsoleCtrlEvent(0, 0);          // CTRL_C_EVENT to the attached console
        System.Threading.Thread.Sleep(200);
        FreeConsole();
        return ok ? 0 : Marshal.GetLastWin32Error();
    }
}
"@
exit ([ConsoleCtrl]::SendCtrlC($ProcessId))
