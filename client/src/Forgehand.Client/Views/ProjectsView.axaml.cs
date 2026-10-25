using Avalonia.Controls;
using Avalonia.Interactivity;
using Avalonia.Input.Platform;
using Forgehand.Client.Core.ViewModels;

namespace Forgehand.Client.Views;

public partial class ProjectsView : UserControl
{
    public ProjectsView()
    {
        InitializeComponent();
    }

    private async void CopyResultClicked(object? sender, RoutedEventArgs e)
    {
        if (DataContext is not ProjectsViewModel { SelectedExecutionResult.HasResult: true } viewModel)
            return;
        var clipboard = TopLevel.GetTopLevel(this)?.Clipboard;
        if (clipboard is not null)
            await clipboard.SetTextAsync(viewModel.SelectedExecutionResult.Content);
    }
}
