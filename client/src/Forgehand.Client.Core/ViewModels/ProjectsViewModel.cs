using System.Collections.ObjectModel;
using CommunityToolkit.Mvvm.ComponentModel;
using CommunityToolkit.Mvvm.Input;
using Forgehand.Client.Core.Api;
using Forgehand.Client.Core.Session;

namespace Forgehand.Client.Core.ViewModels;

/// <summary>A project as returned by the daemon. Fields are displayed, never derived.</summary>
public sealed class ProjectItem
{
    public ProjectItem(ProjectResponse project)
    {
        Name = project.Name ?? string.Empty;
        Id = project.Id;
        RootPath = string.IsNullOrWhiteSpace(project.RootPath) ? null : project.RootPath;
    }

    public string Name { get; }

    public long Id { get; }

    public string? RootPath { get; }

    public bool HasRootPath => RootPath is not null;

    public string IdText => $"ID {Id}";
}

public sealed partial class ProjectsViewModel : ObservableObject
{
    private readonly ForgehandService _service;
    private CancellationTokenSource? _loadCancellation;

    public ProjectsViewModel(ForgehandService service)
    {
        _service = service;
        Projects.CollectionChanged += (_, _) => RaiseDerivedState();
    }

    public ObservableCollection<ProjectItem> Projects { get; } = [];

    [ObservableProperty]
    [NotifyCanExecuteChangedFor(nameof(RefreshCommand))]
    [NotifyPropertyChangedFor(nameof(ShowEmptyState))]
    private bool _isLoading;

    [ObservableProperty]
    [NotifyPropertyChangedFor(nameof(ShowEmptyState))]
    private bool _hasLoaded;

    [ObservableProperty]
    private string? _errorMessage;

    public bool HasError => ErrorMessage is not null;

    public bool ShowProjects => Projects.Count > 0;

    public bool ShowEmptyState => HasLoaded && !IsLoading && ErrorMessage is null && Projects.Count == 0;

    partial void OnErrorMessageChanged(string? value) => RaiseDerivedState();

    [RelayCommand(CanExecute = nameof(CanRefresh))]
    private Task RefreshAsync() => LoadAsync();

    private bool CanRefresh() => !IsLoading;

    public async Task LoadAsync()
    {
        _loadCancellation?.Cancel();
        var cancellation = new CancellationTokenSource();
        _loadCancellation = cancellation;

        IsLoading = true;
        ErrorMessage = null;
        try
        {
            var projects = await _service.GetProjectsAsync(cancellation.Token);
            Projects.Clear();
            foreach (var project in projects)
            {
                Projects.Add(new ProjectItem(project));
            }

            HasLoaded = true;
        }
        catch (ForgehandApiException exception)
        {
            // A failed refresh must not leave the previous authorization result on screen.
            Projects.Clear();
            HasLoaded = false;
            ErrorMessage = exception.Message;
        }
        catch (OperationCanceledException)
        {
        }
        finally
        {
            if (ReferenceEquals(cancellation, _loadCancellation))
            {
                IsLoading = false;
            }
        }
    }

    /// <summary>Discards all project data. Used when the session ends.</summary>
    public void Clear()
    {
        _loadCancellation?.Cancel();
        _loadCancellation = null;
        Projects.Clear();
        IsLoading = false;
        HasLoaded = false;
        ErrorMessage = null;
    }

    private void RaiseDerivedState()
    {
        OnPropertyChanged(nameof(HasError));
        OnPropertyChanged(nameof(ShowProjects));
        OnPropertyChanged(nameof(ShowEmptyState));
    }
}
